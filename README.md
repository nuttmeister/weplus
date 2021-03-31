# weplus

Automated likes and comments using weplus.

Consists of the following resources.

- Lambda function
- S3 bucket
- CW events rules

## Setter

Use the setter located in `./setter` to add persons/emails to automatically comment/like.  
Also see the setter readme for example comments.txt file.

## Build

```shell
make build
```

## Payload

The first time you run the system you need to set `markAsSeen` to true. This will go through all the currently posted
posts and mark them as "seen" by the system and save the state. So only future posts will be commented / liked.

This means that older posts you will still need to manage yourself.

You can omit `markAsSeen` (default: false), `likeRatio` (default: 1.0) and `commentRatio` (default 0.8).  
Only `email` is required.

```json
{
    "email": "your@email.com",
    "markAsSeen": false,
    "likeRatio": 0.85,
    "commentRatio": 0.5
}
```
