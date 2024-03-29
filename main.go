package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/comprehend"
	"github.com/aws/aws-sdk-go-v2/service/comprehend/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	dateFormat = "Mon, 02 Jan 2006 15:04:05 -0700"
	timeFormat = "15:04"
)

var (
	commentRegexp  = regexp.MustCompile(`[ ]*(name|group|duration|type|time|sentiment)[ ]*(==|<=|>=|<|>|~|!=|!~)[ ]*(.*)[ ]*`)
	exerciseRegexp = regexp.MustCompile(`<strong><a href="/users/([0-9]{5})">(.*)</a></strong>[ \n]*</h3>[ \n]*<div class="post-group-name">(.*)</div>[ \n]*<p class="post-status-string"><a href="/statuses/([0-9]{7})"><i class="fas fa-check fa-xs"></i> ([0-9]*) minutes</a> [A-Za-z0-9 <>/":=.,]*<a class="exercise-type" href="/exercises\?exercise_type_name=.*">(.*)</a> <a class="ago-in-words ago timeago" title="[A-Za-z0-9 :\-\+]*" id="exercise-[0-9]{7}-happened-at-ago" data-toggle-id="exercise-[0-9]{7}-happened-at-exact-time">[A-Za-z0-9 ]*</a><a class="ago-in-words exact-time" title="[0-9 \-:\+]*"* id="exercise-[0-9]{7}-happened-at-exact-time" data-toggle-id="exercise-[0-9]{7}-happened-at-ago">([A-Z-a-z0-9, :\+\-]*)</a>`)
	postRegexp     = regexp.MustCompile(`<strong><a href="/users/([0-9]{5})">(.*)</a></strong>[ \n]*</h3>[ \n]*<div class="post-group-name">(.*)</div>[ \n]*<p class="post-status-string"><a href="/statuses/([0-9]{7})"><i class="fas fa-check fa-xs"></i> Post</a> <a class="ago-in-words ago timeago" id="post-[0-9]{7}-happened-at-ago" data-toggle-id="post-[0-9]{7}-happened-at-exact-time">[A-Za-z0-9 ]*</a><a class="ago-in-words exact-time" id="post-[0-9]{7}-happened-at-exact-time" data-toggle-id="post-[0-9]{7}-happened-at-ago">([A-Z-a-z0-9, :\+\-]*)</a>`)
	textRegexp     = regexp.MustCompile(`(?s)<div class="post-body">[ \n]*<p>(.*)</p></div>`)
	userIDRegexp   = regexp.MustCompile(`<a href="/users/([0-9]{5})">[ \n]*<i class="fas fa-chart-bar"></i>[ \n]*My Statistics[ \n]*</a>`)
	tokenRegexp    = regexp.MustCompile(`<meta name="csrf-token" content="([A-Za-z0-9+/=]*)" />`)

	defLikeRatio    = 1.0
	defCommentRatio = 0.8

	defOrigin  = "https://www.weplusapp.com"
	defReferer = "https://www.weplusapp.com/"
	defAccept  = "text/javascript, application/javascript, application/ecmascript, application/x-ecmascript, */*; q=0.01"
)

func main() {
	lambda.Start(handler)
}

func handler(ctx context.Context, inp *input) (string, error) {
	// Create new config.
	cfg, err := new(ctx, 15000)
	if err != nil {
		return "", err
	}

	// Get and decrypt password.
	if err := cfg.parse(inp); err != nil {
		return "", err
	}

	// Load previous states data and comments.
	data, comments, err := cfg.load(inp)
	if err != nil {
		return "", err
	}

	// Get auth token and do auth.
	if err := cfg.login(inp); err != nil {
		return "", err
	}

	// Get group ids.
	groupIds, err := cfg.getFeed(data.Group, "group", "created-at", "all", "", "0")
	if err != nil {
		return "", err
	}

	// Get company ids.
	companyIds, err := cfg.getFeed(data.Company, "company", "created-at", "image-or-video", "", "0")
	if err != nil {
		return "", err
	}

	// Create output slice.
	output := []string{}

	// Process group.
	addGroupIds, addGroupOutput, err := cfg.processGroupFeeds(groupIds, data, comments, inp)
	if err != nil {
		return "", err
	}
	data.Group = append(data.Group, addGroupIds...)
	output = append(output, addGroupOutput...)

	// Process company.
	addCompanyIds, addCompanyOutput, err := cfg.processCompanyFeeds(companyIds, data, comments, inp)
	if err != nil {
		return "", err
	}
	data.Company = append(data.Company, addCompanyIds...)
	output = append(output, addCompanyOutput...)

	// Save state data.
	if err := cfg.save(inp, data); err != nil {
		return "", err
	}

	// Create output for nothing new or mark as seen runs or truncate if it's to long.
	output = checkOutput(output, inp)

	return strings.Join(output, ""), nil
}

type cfg struct {
	ctx        context.Context
	kms        *kms.Client
	s3         *s3.Client
	comprehend *comprehend.Client
	client     *http.Client

	userID   string
	token    string
	password string
	bucket   string
}

func new(ctx context.Context, timeout int) (*cfg, error) {
	cfg := &cfg{ctx: ctx, bucket: os.Getenv("BUCKET")}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("couldn't create a new cookie jar. %w", err)
	}

	cfg.client = &http.Client{
		Timeout: time.Duration(timeout) * time.Millisecond,
		Jar:     jar,
	}

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("couldn't load aws default config. %w", err)
	}

	cfg.kms = kms.NewFromConfig(awsCfg)
	cfg.s3 = s3.NewFromConfig(awsCfg)
	cfg.comprehend = comprehend.NewFromConfig(awsCfg)

	return cfg, nil
}

type input struct {
	Email        string   `json:"email"`
	LikeRatio    *float64 `json:"likeRatio,omitempty"`
	CommentRatio *float64 `json:"commentRatio,omitempty"`
	MarkAsSeen   bool     `json:"markAsSeen"`
}

func (cfg *cfg) parse(inp *input) error {
	if inp.Email == "" {
		return fmt.Errorf("email not set in input")
	}

	if inp.LikeRatio == nil {
		inp.LikeRatio = &defLikeRatio
	}

	if inp.CommentRatio == nil {
		inp.CommentRatio = &defCommentRatio
	}

	pass, err := cfg.getPassword(inp)
	if err != nil {
		return err
	}

	cfg.password = pass
	return nil
}

func (cfg *cfg) processGroupFeeds(groupPosts []*post, data *data, comments []*comment, inp *input) ([]string, []string, error) {
	ids := []string{}
	output := []string{}

	for _, post := range groupPosts {
		if dl, ok := cfg.ctx.Deadline(); ok {
			if time.Now().Add(time.Duration(30) * time.Second).After(dl) {
				fmt.Printf("less then 30 seconds left of deadline. aborting and saving state!\n")
				return ids, output, nil
			}
		}

		doSeen := seen(post.postID, data.Group)
		if !doSeen && !inp.MarkAsSeen {
			if err := cfg.like(post.postID); err != nil {
				return nil, nil, err
			}
			row := fmt.Sprintf("liking group post: %s for %s\n", post.postID, inp.Email)
			output = append(output, row)
			fmt.Printf(row)

			for _, msg := range random(comments, post) {
				comment := replaceComment(msg, post)
				if err := cfg.comment(post.postID, comment); err != nil {
					return nil, nil, err
				}
				row := fmt.Sprintf("commenting '%s' on group post: %s for %s\n", comment, post.postID, inp.Email)
				output = append(output, row)
				fmt.Printf(row)
			}
		}
		if !doSeen {
			ids = append(ids, post.postID)
		}
	}
	return ids, output, nil
}

func (cfg *cfg) processCompanyFeeds(companyPosts []*post, data *data, comments []*comment, inp *input) ([]string, []string, error) {
	ids := []string{}
	output := []string{}

	for _, post := range companyPosts {
		if dl, ok := cfg.ctx.Deadline(); ok {
			if time.Now().Add(time.Duration(30) * time.Second).After(dl) {
				fmt.Printf("less then 30 seconds left of deadline. aborting and saving state!\n")
				return ids, output, nil
			}
		}

		doLike, doComment, doSeen := doAction(post.postID, data.Company, *inp.LikeRatio, *inp.CommentRatio)
		if doComment && !inp.MarkAsSeen {
			if err := cfg.sentiment(post); err != nil {
				fmt.Printf("couldn't get sentiment for text %s. %s", post.text, err.Error())
			}

			doLike = true
			for _, msg := range random(comments, post) {
				comment := replaceComment(msg, post)
				if err := cfg.comment(post.postID, comment); err != nil {
					return nil, nil, err
				}
				row := fmt.Sprintf("commenting '%s' on company post: %s for %s\n", comment, post.postID, inp.Email)
				output = append(output, row)
				fmt.Printf(row)
			}
		}
		if doLike && !inp.MarkAsSeen {
			if err := cfg.like(post.postID); err != nil {
				return nil, nil, err
			}
			row := fmt.Sprintf("liking company post: %s for %s\n", post.postID, inp.Email)
			output = append(output, row)
			fmt.Printf(row)
		}
		if !doSeen {
			ids = append(ids, post.postID)
		}
	}
	return ids, output, nil
}

func (cfg *cfg) getPassword(inp *input) (string, error) {
	str := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(inp.Email, ".", "_"), "-", "_"), "@", "_"))

	raw := os.Getenv(str)
	if raw == "" {
		return "", fmt.Errorf("couldn't read env var %s", str)
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("couldn't base64 decode password for %s", str)
	}

	res, err := cfg.kms.Decrypt(cfg.ctx, &kms.DecryptInput{CiphertextBlob: decoded})
	if err != nil {
		return "", fmt.Errorf("couldn't decrypt password %s. %w", str, err)
	}

	return string(res.Plaintext), nil
}

type data struct {
	Group   []string `json:"group"`
	Company []string `json:"company"`
}

func (cfg *cfg) load(inp *input) (*data, []*comment, error) {
	email := strings.ToLower(inp.Email)
	commentsFile := fmt.Sprintf("%s.comments.txt", email)
	stateFile := fmt.Sprintf("%s.json", email)

	// Read comments data.
	rawComments, err := cfg.download(commentsFile)
	if err != nil {
		return nil, nil, fmt.Errorf("couldn't read comment data. %w", err)
	}

	comments, err := loadComments(rawComments)
	if err != nil {
		return nil, nil, err
	}

	// Read personal state data.
	raw, err := cfg.download(stateFile)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchKey") {
			if inp.MarkAsSeen {
				return &data{}, comments, nil
			}
			return nil, comments, fmt.Errorf("first load needs to be with markAsSeen true. %w", err)
		}
		return nil, comments, fmt.Errorf("couldn't read state data. %w", err)
	}

	d := &data{}
	if err := json.Unmarshal(raw, d); err != nil {
		return nil, comments, fmt.Errorf("couldn't json unmarshal body of state data. %w", err)
	}

	return d, comments, nil
}

type comment struct {
	weight      int
	sentiment   string
	expressions []*expression
	comments    []string
}

type expression struct {
	key     string
	operand string
	value   string
}

func loadComments(raw []byte) ([]*comment, error) {
	comments := []*comment{}

	for _, commentPair := range strings.Split(string(raw), "\n") {
		comment := &comment{}

		rawComment := strings.Split(commentPair, "|")
		// Continue if row doesn't contain valid data.
		if len(rawComment) < 3 {
			continue
		}

		rawWeight := strings.TrimSpace(rawComment[0])
		if rawWeight != "" {
			weight, err := strconv.Atoi(rawWeight)
			if err != nil {
				return nil, fmt.Errorf("couldn't convert weight %s to int. %w", rawWeight, err)
			}
			comment.weight = weight
		}

		exprs := strings.ToLower(strings.TrimSpace(rawComment[1]))
		if exprs != "" {
			for _, expr := range strings.Split(exprs, "&&") {
				// Exit if we found none empty but faulty expression.
				matches := commentRegexp.FindStringSubmatch(expr)
				if len(matches) != 4 {
					return nil, fmt.Errorf("error in expression for comment row %s", rawComment)
				}

				// Special case for sentiment.
				if strings.TrimSpace(matches[1]) == "sentiment" && strings.TrimSpace(matches[2]) == "==" {
					comment.sentiment = strings.TrimSpace(matches[3])
					continue
				}

				comment.expressions = append(comment.expressions, &expression{
					key:     strings.TrimSpace(matches[1]),
					operand: strings.TrimSpace(matches[2]),
					value:   strings.TrimSpace(matches[3]),
				})
			}
		}

		for _, str := range rawComment[2:] {
			str = strings.TrimSpace(str)
			if str != "" {
				comment.comments = append(comment.comments, str)
			}
		}

		comments = append(comments, comment)
	}

	return comments, nil
}

func (cfg *cfg) download(file string) ([]byte, error) {
	resp, err := cfg.s3.GetObject(cfg.ctx, &s3.GetObjectInput{Bucket: &cfg.bucket, Key: &file})
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchKey") {
			return nil, err
		}
		return nil, fmt.Errorf("couldn't download file from s3://%s/%s. %w", cfg.bucket, file, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("couldn't read body of file from s3://%s/%s. %w", cfg.bucket, file, err)
	}

	return raw, nil
}

func (cfg *cfg) save(inp *input, data *data) error {
	email := strings.ToLower(inp.Email)

	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("couldn't json marshal state data for %s. %w", email, err)
	}

	file := fmt.Sprintf("%s.json", strings.ToLower(email))
	_, err = cfg.s3.PutObject(cfg.ctx, &s3.PutObjectInput{
		Bucket: &cfg.bucket,
		Key:    &file,
		Body:   bytes.NewReader(raw),
	})
	if err != nil {
		return fmt.Errorf("couldn't save state data to s3://%s/%s. %w", cfg.bucket, file, err)
	}

	return nil
}

type request struct {
	method      string
	url         string
	body        []byte
	contentType string
	accept      string
	origin      string
	referer     string
}

func newRequest(req *request) (*http.Request, error) {
	r, err := http.NewRequest(req.method, req.url, bytes.NewReader(req.body))
	if err != nil {
		return nil, fmt.Errorf("couldn't create new http request for %s %s. %w", req.method, req.url, err)
	}

	r.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 11.2; rv:86.0) Gecko/20100101 Firefox/86.0")

	if req.contentType != "" {
		r.Header.Set("Content-Type", req.contentType)
	}

	if req.accept != "" {
		r.Header.Set("Accept", req.accept)
	}

	if req.origin != "" {
		r.Header.Set("Origin", req.origin)
	}

	if req.referer != "" {
		r.Header.Set("Referer", req.referer)
	}

	return r, nil
}

func (cfg *cfg) checkToken(body string) {
	matches := tokenRegexp.FindStringSubmatch(body)
	if len(matches) == 2 {
		cfg.token = matches[1]
	}
}

func (cfg *cfg) checkUserID(body string) {
	matches := userIDRegexp.FindStringSubmatch(body)
	if len(matches) == 2 {
		cfg.userID = matches[1]
	}
}

func (cfg *cfg) login(inp *input) error {
	if err := cfg.setAuthToken(); err != nil {
		return err
	}

	return cfg.auth(inp.Email, cfg.password)
}

func (cfg *cfg) setAuthToken() error {
	req, err := newRequest(&request{
		method: "GET",
		url:    "https://www.weplusapp.com/login",
	})
	if err != nil {
		return err
	}

	resp, err := cfg.client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't send http request to %s. %w", req.URL.String(), err)
	}
	defer resp.Body.Close()

	raw, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("couldn't read response body for %s. %w", req.URL.String(), err)
	}
	body := string(raw)

	if resp.StatusCode != 200 {
		return fmt.Errorf("expected status code 200 from get session but got %d. %s", resp.StatusCode, body)
	}

	cfg.checkToken(body)

	return nil
}

func (cfg *cfg) auth(username string, password string) error {
	payload := url.Values{}
	payload.Set("utf8", "✓")
	payload.Set("authenticity_token", cfg.token)
	payload.Set("email", username)
	payload.Set("password", password)
	payload.Set("commit", "Logga+in")

	req, err := newRequest(&request{
		method:      "POST",
		url:         "https://www.weplusapp.com/sessions",
		body:        []byte(payload.Encode()),
		contentType: "application/x-www-form-urlencoded",
		origin:      "https://www.weplusapp.com",
		referer:     "https://www.weplusapp.com/login",
	})
	if err != nil {
		return err
	}

	resp, err := cfg.client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't send http request to %s. %w", req.URL.String(), err)
	}
	defer resp.Body.Close()

	raw, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("couldn't read response body for %s. %w", req.URL.String(), err)
	}
	body := string(raw)

	if resp.StatusCode != 200 {
		return fmt.Errorf("expected status code 200 from login but got %d. %s", resp.StatusCode, body)
	}

	cfg.checkToken(body)
	cfg.checkUserID(body)

	if strings.Contains(body, "Email eller lösenord är ogiltiga") {
		return fmt.Errorf("wrong username or password")
	}

	return nil
}

type post struct {
	group            bool
	exercise         bool
	date             time.Time
	postID           string
	userID           string
	name             string
	groupName        string
	trainingDuration string
	trainingType     string
	text             string
	sentiment        types.SentimentType
}

func (cfg *cfg) getFeed(prev []string, feedType string, sort string, filter string, query string, offset string) ([]*post, error) {
	qs := url.Values{}
	qs.Set("utf8", "✓")
	qs.Set("type", feedType)
	qs.Set("sort", sort)
	qs.Set("filter", filter)
	qs.Set("query", query)
	qs.Set("only_items", "true")
	qs.Set("offset", offset)
	req, err := newRequest(&request{
		method:  "GET",
		url:     fmt.Sprintf("https://www.weplusapp.com/feed?%s", qs.Encode()),
		referer: "https://www.weplusapp.com/",
	})
	if err != nil {
		return nil, err
	}

	resp, err := cfg.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't send http request to %s. %w", req.URL.String(), err)
	}
	defer resp.Body.Close()

	raw, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("couldn't read response body for %s. %w", req.URL.String(), err)
	}
	body := string(raw)

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("expected status code 200 from get feeds but got %d. %s", resp.StatusCode, body)
	}

	cfg.checkToken(body)

	ids := []*post{}
	added := []string{}
	done := false

	exerciseMatches := exerciseRegexp.FindAllStringSubmatch(body, -1)
	postMatches := postRegexp.FindAllStringSubmatch(body, -1)

	for _, match := range append(exerciseMatches, postMatches...) {
		data := &post{}
		rawDate := ""

		switch len(match) {
		case 8:
			data.trainingDuration = match[5]
			data.trainingType = match[6]
			rawDate = match[7]
			data.exercise = true
		case 6:
			rawDate = match[5]
		default:
			fmt.Printf("something went wrong when matching feed. expected length to be 6 or 8 but got %d\n", len(match))
			continue
		}

		data.postID = match[4]
		data.userID = match[1]

		// If at least one of the ids has been seen we can stop
		// downloading new posts since we sort on created at.
		if seen(data.postID, prev) {
			done = true
		}

		// If postID has been seen skip any further processing.
		if seen(data.postID, added) {
			continue
		}

		// Skip commenting and liking your own posts.
		if data.userID == cfg.userID {
			continue
		}

		data.name = match[2]
		data.groupName = match[3]
		data.sentiment = types.SentimentTypeNeutral

		date, err := time.Parse(dateFormat, rawDate)
		if err != nil {
			fmt.Printf("couldn't parse date from string %s for post id %s. skipping\n", rawDate, data.postID)
			continue
		}
		data.date = date

		// Mark group feed as group post.
		if feedType == "group" {
			data.group = true
		}

		// Get comment text.
		text, err := cfg.getComment(data.postID)
		if err != nil {
			fmt.Printf("couldn't get comment text for post id %s. ignoring sentiment on post. %s\n", data.postID, err.Error())
		}
		data.text = text

		ids = append(ids, data)
		added = append(added, data.postID)
	}

	// If done is true we can just return and not process anymore posts.
	if !done {
		regMore, err := regexp.Compile(fmt.Sprintf(
			`<li class="feed-more-item" data-type="%s" data-offset="([0-9]*)" data-limit="12" data-sort="%s" data-filter="%s">`,
			feedType, sort, filter,
		))
		if err != nil {
			return nil, err
		}
		moreMatches := regMore.FindStringSubmatch(body)
		if len(moreMatches) == 2 {
			new, err := cfg.getFeed(prev, feedType, sort, filter, query, moreMatches[1])
			if err != nil {
				return nil, err
			}
			ids = append(ids, new...)
		}
	}

	return ids, nil
}

func (cfg *cfg) getComment(postID string) (string, error) {
	req, err := newRequest(&request{
		method:  "GET",
		url:     fmt.Sprintf("https://www.weplusapp.com/statuses/%s?layout=false", postID),
		referer: "https://www.weplusapp.com/",
	})
	if err != nil {
		return "", err
	}

	resp, err := cfg.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("couldn't send http request to %s. %w", req.URL.String(), err)
	}
	defer resp.Body.Close()

	raw, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("couldn't read response body for %s. %w", req.URL.String(), err)
	}
	body := string(raw)

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("expected status code 200 from get feeds but got %d. %s", resp.StatusCode, body)
	}

	cfg.checkToken(body)

	textMatches := textRegexp.FindStringSubmatch(body)
	if len(textMatches) != 2 {
		return "", fmt.Errorf("expected text matches to be 2 but got %d", len(textMatches))
	}

	return textMatches[1], nil
}

func (cfg *cfg) like(id string) error {
	payload := url.Values{}
	payload.Set("like[status_id]", id)
	payload.Set("link_css_id", fmt.Sprintf("like-status-%s", id))

	req, err := newRequest(&request{
		method:      "POST",
		url:         "https://www.weplusapp.com/likes",
		body:        []byte(payload.Encode()),
		contentType: "application/x-www-form-urlencoded; charset=UTF-8",
		accept:      defAccept,
		origin:      "https://www.weplusapp.com",
		referer:     "https://www.weplusapp.com/",
	})
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-CSRF-Token", cfg.token)

	resp, err := cfg.client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't send http request to %s. %w", req.URL.String(), err)
	}
	defer resp.Body.Close()

	raw, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("couldn't read response body for %s. %w", req.URL.String(), err)
	}
	body := string(raw)

	if resp.StatusCode != 200 {
		return fmt.Errorf("expected status code 200 from like but got %d. %s", resp.StatusCode, body)
	}

	cfg.checkToken(body)

	return nil
}

func (cfg *cfg) comment(id string, comment string) error {
	payload := url.Values{}
	payload.Set("comment[body]", comment)
	payload.Set("comment[status_id]", id)
	payload.Set("comments_css_id", fmt.Sprintf("comments-list-%s", id))

	req, err := newRequest(&request{
		method:      "POST",
		url:         "https://www.weplusapp.com/comments",
		body:        []byte(payload.Encode()),
		contentType: "application/x-www-form-urlencoded; charset=UTF-8",
		accept:      defAccept,
		origin:      defOrigin,
		referer:     defReferer,
	})
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-CSRF-Token", cfg.token)

	resp, err := cfg.client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't send http request to %s. %w", req.URL.String(), err)
	}
	defer resp.Body.Close()

	raw, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("couldn't read response body for %s. %w", req.URL.String(), err)
	}
	body := string(raw)

	if resp.StatusCode != 200 {
		return fmt.Errorf("expected status code 200 from comment but got %d. %s", resp.StatusCode, body)
	}

	cfg.checkToken(body)

	return nil
}

func seen(id string, slice []string) bool {
	for _, ssid := range slice {
		if id == ssid {
			return true
		}
	}
	return false
}

func doAction(id string, slice []string, likeRatio float64, commentRatio float64) (bool, bool, bool) {
	doSeen := seen(id, slice)
	if doSeen {
		return false, false, doSeen
	}

	rand.Seed(time.Now().UnixNano())
	like := rand.Float64() < likeRatio
	rand.Seed(time.Now().UnixNano())
	comment := rand.Float64() < commentRatio

	return like, comment, doSeen
}

func random(comments []*comment, post *post) []string {
	valid := validComments(comments, post)
	if len(valid) == 0 {
		fmt.Printf("no comments matched for post: '%+v'\n", *post)
		return []string{}
	}

	rand.Seed(time.Now().UnixNano())
	return valid[rand.Intn(len(valid))].comments
}

func validComments(comments []*comment, post *post) []*comment {
	valid := []*comment{}
	curWeight := 0

	for _, comnt := range comments {
		// If expression is empty and it's post or group don't add.
		if len(comnt.expressions) == 0 && (!post.exercise || post.group) {
			continue
		}

		// If sentiment is negative or mixed and sentiment isn't marked as negative simply don't add it.
		switch post.sentiment {
		case types.SentimentTypeMixed:
			if comnt.sentiment != "neg" {
				continue
			}
		case types.SentimentTypeNegative:
			if comnt.sentiment != "neg" {
				continue
			}
		}

		add := []bool{}
		for _, expr := range comnt.expressions {
			// Hand group exercises and group post by only allowing "type == group" and "type == group-post".
			if post.group {
				switch {
				case expr.key == "type" && expr.operand == "==" && expr.value == "group" && post.exercise:
					add = append(add, true)
				case expr.key == "type" && expr.operand == "==" && expr.value == "group-post" && !post.exercise:
					add = append(add, true)
				default:
					add = append(add, false)
				}
				continue
			}

			// If the post is a none exercise post only mark "type == post" as valid.
			if !post.exercise {
				switch {
				case expr.key == "type" && expr.operand == "==" && expr.value == "post":
					add = append(add, true)
				default:
					add = append(add, false)
				}
				continue
			}

			// If there was no expression just pass the comment.
			if expr.key == "" && post.exercise {
				add = append(add, true)
				continue
			}

			switch expr.key {
			case "name":
				switch expr.operand {
				case "==":
					if expr.value == strings.ToLower(post.name) {
						add = append(add, true)
						continue
					}
				case "~":
					if strings.Contains(strings.ToLower(post.name), expr.value) {
						add = append(add, true)
						continue
					}
				case "!=":
					if expr.value != strings.ToLower(post.name) {
						add = append(add, true)
						continue
					}
				case "!~":
					if !strings.Contains(strings.ToLower(post.name), expr.value) {
						add = append(add, true)
						continue
					}
				}
			case "group":
				switch expr.operand {
				case "==":
					if expr.value == strings.ToLower(post.groupName) {
						add = append(add, true)
						continue
					}
				case "~":
					if strings.Contains(strings.ToLower(post.groupName), expr.value) {
						add = append(add, true)
						continue
					}
				case "!=":
					if expr.value != strings.ToLower(post.groupName) {
						add = append(add, true)
						continue
					}
				case "!~":
					if !strings.Contains(strings.ToLower(post.groupName), expr.value) {
						add = append(add, true)
						continue
					}
				}
			case "duration":
				exprDur, err := strconv.Atoi(expr.value)
				if err != nil {
					add = append(add, false)
					fmt.Printf("couldn't convert expression duration %s to int, continuing\n", expr.value)
					continue
				}

				postDur, err := strconv.Atoi(post.trainingDuration)
				if err != nil {
					add = append(add, false)
					fmt.Printf("couldn't convert post duration %s to int, continuing\n", post.trainingDuration)
					continue
				}

				switch expr.operand {
				case "==":
					if postDur == exprDur {
						add = append(add, true)
						continue
					}
				case ">=":
					if postDur >= exprDur {
						add = append(add, true)
						continue
					}
				case ">":
					if postDur > exprDur {
						add = append(add, true)
						continue
					}
				case "<=":
					if postDur <= exprDur {
						add = append(add, true)
						continue
					}
				case "<":
					if postDur < exprDur {
						add = append(add, true)
						continue
					}
				}
			case "time":
				exprDate, err := time.Parse(timeFormat, expr.value)
				if err != nil {
					add = append(add, false)
					fmt.Printf("couldn't convert expression time %s to time.Time, continuing\n", expr.value)
					continue
				}
				exprSeconds := (exprDate.UTC().Hour() * 60) + exprDate.Minute()
				postSeconds := (post.date.UTC().Hour() * 60) + post.date.Minute()

				switch expr.operand {
				case "==":
					if postSeconds == exprSeconds {
						add = append(add, true)
						continue
					}
				case ">=":
					if postSeconds >= exprSeconds {
						add = append(add, true)
						continue
					}
				case ">":
					if postSeconds > exprSeconds {
						add = append(add, true)
						continue
					}
				case "<=":
					if postSeconds <= exprSeconds {
						add = append(add, true)
						continue
					}
				case "<":
					if postSeconds < exprSeconds {
						add = append(add, true)
						continue
					}
				}
			case "type":
				switch expr.operand {
				case "==":
					if expr.value == strings.ToLower(post.trainingType) {
						add = append(add, true)
						continue
					}
				case "~":
					if strings.Contains(strings.ToLower(post.trainingType), expr.value) {
						add = append(add, true)
						continue
					}
				case "!=":
					if expr.value != strings.ToLower(post.trainingType) {
						add = append(add, true)
						continue
					}
				case "!~":
					if !strings.Contains(strings.ToLower(post.trainingType), expr.value) {
						add = append(add, true)
						continue
					}
				}
			}

			add = append(add, false)
		}

		if isValid(add) {
			// Either reset valid or add depending on weight.
			// If weight is lower than current don't add.
			switch {
			case comnt.weight > curWeight:
				valid = []*comment{comnt}
				curWeight = comnt.weight
			case comnt.weight == curWeight:
				valid = append(valid, comnt)
			}
		}
	}

	return valid
}

func checkOutput(output []string, inp *input) []string {
	if len(output) == 0 {
		switch inp.MarkAsSeen {
		case true:
			output = append(output, "state saved up until now! you can now run in it normally")
		case false:
			output = append(output, "nothing liked or commented since last run!")
		}
	}

	if len(output) > 200 {
		output = output[0:199]
		output = append(output, "output was truncated due to it being to long ...")
	}

	return output
}

func replaceComment(comment string, post *post) string {
	str := strings.ReplaceAll(comment, "{{Name}}", post.name)
	str = strings.ReplaceAll(str, "{{name}}", post.name)
	str = strings.ReplaceAll(str, "{{Group}}", post.groupName)
	str = strings.ReplaceAll(str, "{{group}}", post.groupName)
	str = strings.ReplaceAll(str, "{{Duration}}", post.trainingDuration)
	str = strings.ReplaceAll(str, "{{duration}}", post.trainingDuration)
	str = strings.ReplaceAll(str, "{{Type}}", post.trainingType)
	str = strings.ReplaceAll(str, "{{type}}", post.trainingType)
	str = strings.ReplaceAll(str, "{{Time}}", post.date.Format(timeFormat))
	str = strings.ReplaceAll(str, "{{time}}", post.date.Format(timeFormat))
	return str
}

func isValid(slice []bool) bool {
	for _, cur := range slice {
		if !cur {
			return false
		}
	}
	return true
}

func (cfg *cfg) sentiment(post *post) error {
	if post.text == "" {
		return nil
	}

	lang, err := cfg.comprehend.DetectDominantLanguage(cfg.ctx, &comprehend.DetectDominantLanguageInput{
		Text: &post.text,
	})
	if err != nil {
		return fmt.Errorf("couldn't detect language for %s. %w", post.text, err)
	}

	langCode := language(lang.Languages, supportedLanguages)

	res, err := cfg.comprehend.DetectSentiment(cfg.ctx, &comprehend.DetectSentimentInput{
		Text:         &post.text,
		LanguageCode: langCode,
	})
	if err != nil {
		return fmt.Errorf("couldn't get sentiment of text %s. %w", post.text, err)
	}

	post.sentiment = res.Sentiment
	return nil
}

var supportedLanguages = []string{"de", "en", "es", "it", "pt", "fr", "ja", "ko", "hi", "ar", "zh", "zh-TW"}

func language(languages []types.DominantLanguage, supported []string) types.LanguageCode {
	lang, top := "en", float32(0)
	for _, detectedLang := range languages {
		if *detectedLang.Score > top {
			lang, top = *detectedLang.LanguageCode, *detectedLang.Score
		}
	}

	for _, supportedLang := range supported {
		if lang == supportedLang {
			return types.LanguageCode(lang)
		}
	}

	return types.LanguageCode("en")
}
