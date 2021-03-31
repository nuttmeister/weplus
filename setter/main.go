package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchevents"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchevents/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type cfg struct {
	KeyAlias string `json:"keyAlias"`
	FuncArn  string `json:"funcArn"`
	Bucket   string `json:"bucket"`

	ctx    context.Context
	kms    *kms.Client
	s3     *s3.Client
	lambda *lambda.Client
	cw     *cloudwatchevents.Client
}

func main() {
	cfg, err := configure()
	if err != nil {
		log.Fatal(err)
	}

	email, password, commentsFile, createEvent, enableEvent, disableEvent, err := input()
	if err != nil {
		flag.Usage()
		os.Exit(1)
	}

	// Read and upload comments if comments file as supplied.
	if commentsFile != "" {
		comments, err := readComments(commentsFile)
		if err != nil {
			log.Fatal(err)
		}

		if err := cfg.upload(email, comments); err != nil {
			log.Fatal(err)
		}

		fmt.Printf("finished uploading comments for %s\n", email)
	}

	// Set password if it was supplied.
	if password != "" {
		encryptedPassword, err := cfg.encrypt(password)
		if err != nil {
			log.Fatal(err)
		}

		if err := cfg.setPassword(email, encryptedPassword); err != nil {
			log.Fatal(err)
		}

		fmt.Printf("finished setting password for %s\n", email)
	}

	// Create or update the cw event if event was true.
	if createEvent {
		if err := cfg.createEvent(email); err != nil {
			log.Fatal(err)
		}

		fmt.Printf("finished creating / updating event for %s. state is disabled, you must also enable it\n", email)
	}

	// Update the state of the event.
	if enableEvent || disableEvent {
		state, stateText := false, "disabled"
		if enableEvent && !disableEvent {
			state = true
			stateText = "enabled"
		}

		if err := cfg.enableEvent(email, state); err != nil {
			log.Fatal(err)
		}

		fmt.Printf("event for %s is now %s\n", email, stateText)
	}
}

func configure() (*cfg, error) {
	raw, err := os.ReadFile("config.json")
	if err != nil {
		return nil, err
	}

	cfg := &cfg{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, err
	}

	cfg.ctx = context.Background()
	awsCfg, err := config.LoadDefaultConfig(cfg.ctx)
	if err != nil {
		log.Fatal(err)
	}

	cfg.s3 = s3.NewFromConfig(awsCfg)
	cfg.kms = kms.NewFromConfig(awsCfg)
	cfg.lambda = lambda.NewFromConfig(awsCfg)
	cfg.cw = cloudwatchevents.NewFromConfig(awsCfg)

	return cfg, nil
}

func input() (string, string, string, bool, bool, bool, error) {
	email := flag.String("email", "", "the email to upload data for [*required]")
	commentsFile := flag.String("comments", "", "the comments file to use. leave empty to not upload comments")
	password := flag.String("password", "", "the password to set. leave empty to not update password")
	createEvent := flag.Bool("create-event", false, "use this flag to create or update the event")
	enableEvent := flag.Bool("enable-event", false, "use this flag to set the event as enabled")
	disableEvent := flag.Bool("disable-event", false, "use this flag to set the event as disabled")
	flag.Parse()

	switch {
	case *email == "":
		return "", "", "", false, false, false, fmt.Errorf("input email is required")
	}

	return *email, *password, *commentsFile, *createEvent, *enableEvent, *disableEvent, nil
}

func readComments(commentsFile string) ([]byte, error) {
	comments, err := os.ReadFile(commentsFile)
	if err != nil {
		return nil, err
	}

	for _, b := range comments {
		if b == '\r' {
			return nil, fmt.Errorf("%s is in CRLF should be LF", commentsFile)
		}
	}

	return comments, nil
}

func (cfg *cfg) upload(email string, comments []byte) error {
	commentsFile := fmt.Sprintf("%s.comments.txt", strings.ToLower(email))

	_, err := cfg.s3.PutObject(cfg.ctx, &s3.PutObjectInput{
		Bucket: &cfg.Bucket,
		Key:    &commentsFile,
		Body:   bytes.NewReader(comments),
	})

	return err
}

func (cfg *cfg) encrypt(password string) (string, error) {
	resp, err := cfg.kms.Encrypt(cfg.ctx, &kms.EncryptInput{
		KeyId:     &cfg.KeyAlias,
		Plaintext: []byte(password),
	})
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(resp.CiphertextBlob), nil
}

func (cfg *cfg) setPassword(email string, encryptedPassword string) error {
	key := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(email, ".", "_"), "-", "_"), "@", "_"))

	fnCfg, err := cfg.lambda.GetFunctionConfiguration(cfg.ctx, &lambda.GetFunctionConfigurationInput{
		FunctionName: &cfg.FuncArn,
	})
	if err != nil {
		return err
	}

	environ := fnCfg.Environment.Variables
	environ[key] = encryptedPassword

	_, err = cfg.lambda.UpdateFunctionConfiguration(cfg.ctx, &lambda.UpdateFunctionConfigurationInput{
		FunctionName: &cfg.FuncArn,
		Environment: &types.Environment{
			Variables: environ,
		},
	})

	return err
}

func (cfg *cfg) createEvent(email string) error {
	slice := strings.Split(strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(email, ".", "_"), "-", "_")), "@")

	rand.Seed(time.Now().UnixNano())
	random := rand.Intn(60)

	name := fmt.Sprintf("save-the-hawk-%s", slice[0])
	cron := fmt.Sprintf("cron(%d * * * ? *)", random)

	payload := fmt.Sprintf(`{"email":"%s"}`, email)

	resp, err := cfg.cw.PutRule(cfg.ctx, &cloudwatchevents.PutRuleInput{
		Name:               &name,
		ScheduleExpression: &cron,
		State:              cwtypes.RuleStateDisabled,
	})
	if err != nil {
		return err
	}

	_, err = cfg.cw.PutTargets(cfg.ctx, &cloudwatchevents.PutTargetsInput{
		Rule: &name,
		Targets: []cwtypes.Target{
			{
				Id:    &name,
				Arn:   &cfg.FuncArn,
				Input: &payload,
			},
		},
	})
	if err != nil {
		return err
	}

	action := "lambda:InvokeFunction"
	principal := "events.amazonaws.com"

	_, err = cfg.lambda.RemovePermission(cfg.ctx, &lambda.RemovePermissionInput{
		StatementId:  &name,
		FunctionName: &cfg.FuncArn,
	})
	if err != nil {
		if !strings.Contains(err.Error(), "is not found in resource policy") {
			return err
		}
	}

	_, err = cfg.lambda.AddPermission(cfg.ctx, &lambda.AddPermissionInput{
		StatementId:  &name,
		FunctionName: &cfg.FuncArn,
		Action:       &action,
		Principal:    &principal,
		SourceArn:    resp.RuleArn,
	})

	return err
}

func (cfg *cfg) enableEvent(email string, enabled bool) error {
	slice := strings.Split(strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(email, ".", "_"), "-", "_")), "@")
	name := fmt.Sprintf("save-the-hawk-%s", slice[0])

	var err error
	switch enabled {
	case true:
		_, err = cfg.cw.EnableRule(cfg.ctx, &cloudwatchevents.EnableRuleInput{
			Name: &name,
		})
	case false:
		_, err = cfg.cw.DisableRule(cfg.ctx, &cloudwatchevents.DisableRuleInput{
			Name: &name,
		})
	}

	return err
}
