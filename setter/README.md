# setter

Quickly and easily set your password and upload your comments file to the we+ automator.

## configuration

You must configure some basic information regarding what function to update and what bucket to upload files to
and what kms alias to use. This id done in the file `config.json`.

### config.json example

```json
{
    "keyAlias": "alias/my-kms-key",
    "FuncArn": "arn:aws:lambda:us-east-1:111111111111:function:my-function",
    "Bucket": "my-bucket"
}
```

## comments file

Create a file called `comments.txt` (or whatever you want it to be called) that contains the comments you want to use for your user.  
The file must be in the following format.

The file must be in `LF` line endings. Otherwise the program will produce an error.

### Expressions
```text
expression | comment 1 | comment 2 | comment 3 ...
```

Where expression can be empty to allow the comment to be used always.  
In that case you must leave an empty first filed... Such as `| comment 1 | comment 2`.

Expressions are written as `KEY OPERAND VALUE`, example `group == @Save the Hawk Foundation`.  
The following keys can be used `name`, `group`, `type` and `duration`.

The keys `name`, `group` and `type` only supports the `==` operand.  
The `duration` key supports `==`, `>=`, `<=` `>` and `<`.

So to match on exercises over 90 minutes you would write `duration > 90`.

Here is an example file.

### Variable substitution

You can substitute variables in the comments with data from the post.  
The following are supported:

`{{Name}}` == Name of the poster  
`{{Group}}` == Group the poster belongs to  
`{{Type}}` == Workout type  
`{{Duration}}` == Length in minutes of the workout  

### Example file

```text
| 👍👍👍 | 🙌🙌
| 💪💪 | Keep it up!
| 🙌 👍
| One step closer to victory!
| 👍👍👍, Good job! {{Duration}} minutes closer to victory! (But we will win... 😅)
| 🙌🙌🙌
| 🙌🙌🙌 | Always good with some {{Type}}-workout!
duration < 45 | 👍 | Doing good!
duration > 60 | 👍🙌 | {{Duration}} minutes doing {{Type}}-workout 🙌🙌🙌
duration > 90 | 👍👍👍 | Over 1,5h of workout! Thats really good!
duration > 120 | Damn... {{Duration}} minutes! You're going for the win! | 💪💪💪
duration > 60 | Really good and long workout! But is it enough to beat the us?
name == Big Boss | Big Boss, you're awesome! 💪💪💪 Remember my kind words when it's time to talk salary!
group == @Competitors | You are going down {{Group}}!
```

## Running

Login in to your AWS account and make sure it's set as the default profile for the current shell.  
You can run the different commands below once at at a time or all together.

the parameter `email` is always required.

### Set Password

```shell
./setter --email 'my-email@example.com' --password 'my-password'
```

### Upload comments

```shell
./setter --email 'my-email@example.com' --comments comments.txt
```

### Create CW Event

The default state of the created event is `DISABLED`.

```shell
./setter --email 'my-email@example.com' --create-event
```

### Enable CW Event

```shell
./setter --email 'my-email@example.com' --enable-event
```

### Disable CW Event

```shell
./setter --email 'my-email@example.com' --disable-event
```
