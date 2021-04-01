# setter

Quickly and easily set your password and upload your comments file to the we+ automator.

## configuration

You must configure some basic information regarding what function to update and what bucket to upload files to
and what kms alias to use. This id done in the file `config.json`.

### config.json example

```json
{
    "keyAlias": "alias/my-kms-key",
    "funcArn": "arn:aws:lambda:us-east-1:111111111111:function:my-function",
    "bucket": "my-bucket"
}
```

## comments file

Create a file called `comments.txt` (or whatever you want it to be called) that contains the comments you want to use for your user.  
The file must be in the following format.

The file must be in `LF` line endings. Otherwise the program will produce an error.

This is the current comments file format.

```text
weight | expression | comment 1 | comment 2 | comment 3 ...
```

### Weight

If weight is left empty it will default to `0`. Which is the lowest weight.
Of all the matching comments a random will only be selected among those with highest weight.

So imagine the below comments.

```text
0  | | comment1
0  | | comment2
10 | | comment3
10 | | comment4
0  | | comment5
```

In this case, the program would only ever select between `comment3` and `comment4` due to it having the same (highest) weight than the reset of the comments.

### Expressions

Expressions can be empty to allow the comment to be used always.  
In that case you must leave an empty first filed... Such as `| comment 1 | comment 2`.

Expressions are written as `KEY OPERAND VALUE`, example `group == @Save the Hawk Foundation`.  
The following keys can be used `name`, `group`, `type`, `duration` and `time`.

The keys `name`, `group` and `type` only supports the `==` operand.  
The `duration` and `time` keys supports `==`, `>=`, `<=` `>` and `<`.

So to match on exercises over 90 minutes you would write `duration > 90`.

`time` should be noted in `hh:mm` format and only checks time of day in `UTC` time.

You can also have multiple expressions by chaning them together with `&&`.  
Only `&&` (AND) is supported, so you cannot do OR any advanced checks with `(expr1 && expr2) || expr3`.

For example `type == walking && duration > 90`.

### Posts (None exercise posts)

To support posts (they don't include all the metadata normal exercises do) you must at least have a few comments
that have the expression `type == post` in them. It's the only way the function will now the comment
doesn't include any substitutions for unsupported variables.

### Group Exercises / Group Posts (None exercise posts)

To support group comments, because you might want something else than the normal comments or in a different language.  
You will need to have comments as `type == group` (for group exercise entries) and `type == group-post`, if no comments
with these expression exists no comment will be made on the group feed.  
If it's a `group-post` entry the same limitation as the regular `post` (as stated above) apply.

### Variable substitution

You can substitute variables in the comments with data from the post.  
The following are supported:

`{{Name}}` == Name of the poster  
`{{Group}}` == Group the poster belongs to  
`{{Type}}` == Workout type  
`{{Duration}}` == Length in minutes of the workout

### Example file

```text
|| 👍👍👍 | 🙌🙌
|| 💪💪 | Keep it up!
|| 🙌 👍
|| One step closer to victory!
|| 👍👍👍, Good job! {{Duration}} minutes closer to victory! (But we will win... 😅)
|| 🙌🙌🙌
|| 🙌🙌🙌 | Always good with some {{Type}}-workout!
|duration < 45 | 👍 | Doing good!
|duration > 60 | 👍🙌 | {{Duration}} minutes doing {{Type}}-workout 🙌🙌🙌
|duration > 90 | 👍👍👍 | Over 1,5h of workout! Thats really good!
100 | duration > 120 | Damn... {{Duration}} minutes! You're going for the win! | 💪💪💪
100 | duration > 60 | Really good and long workout! But is it enough to beat the us?
| duration >= 90 && type == yoga | Thats a long and good Yoga pass | 🙏🙏🙏
100 | name == Big Boss | Big Boss, you're awesome! 💪💪💪 Remember my kind words when it's time to talk salary!
100 | group == @Competitors | You are going down {{Group}}!
| type == post | 👍👍👍 | 🙌🙌
| type == post | 🙌🙌 | 👍👍
| type == group-post | 💪💪💪 | Lets go boys and girls!
| type == group | 🙌🙌🙌 | {{Duration}} minutes! Lets win this!
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
