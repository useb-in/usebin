# ![](./static/assets/favicon-32x32.png) [useb.in](https://useb.in)

Encrypted pastebin stored on Usenet.

## Example Config

```json5
// $HOME/.config/usebin/config.json
{
    // The interface address to listen to
    "Host": "0.0.0.0",
    // The port to listen to
    "Port": 8080,
    // Your NNTP server connection infos
    "NNTPServers": [
        {
            // The host and port of the NNTP server
            "Host": "news.example.com:119",
            "User": "user",
            "Pass": "pass",
            // Whether the connection should use TLS encryption
            "TLS": false,
            // Whether the server can be used for posting
            "Posting": true,
            // Maximum number of connections for this server
            "Connections": 50,
        }
    ],
    // How long can connections to be idle until being closed, in seconds
    "IdleConnExpiry": 60,
    // The newsgroup to post to if not set explicitly in the request
    "DefaultNewsgroup": "alt.binaries.misc",
    // Max number of bytes an article can have, limited on article get and post
    "ArticleSizeLimit": 4194304,
    // If set, will use the following X509 PEM encoded certificate and key files to enable TLS for the server
    // "CertFile": "./path/to/cert.pem",
    // "KeyFile": "./path/to/key.pem",
}
```

## API

### `GET /m/<Message-ID>.csv`

Get the full article by Message-ID. Any NNTP headers will be prefixed by `X-Usenet-` and included together in the HTTP
response headers. The returned article body is dot-decoded, and `<CR> <LF>` line endings are converted to a single
`<LF>`. The `Content-Length` HTTP header is set to be the number of bytes of the dot-decoded article body.

### `HEAD /m/<Message-ID>.csv`

Get the article headers and `Content-Length` without the article body. However, in order to calculate the
`Content-Length`, the full article is still being download by the Usebin server. If `Content-Length` is not required,
use the `GET /h/<Message-ID>.csv` API instead, which will return only the article headers without `Content-Length`.

### `POST /m/<Message-ID>.csv`

Post an article with the specified Message-ID. The HTTP body will be dot-encoded by the Usebin server, also line ending
will be normalized to `<CR> <LF>` before sending to an NNTP server. However the requesting client should keep the
article lines under permitted length, usually under 127 bytes per line. Any HTTP header starting with `X-Usenet-` will
be stripped off its prefix and set as an NNTP header and send to the NNTP server.

#### URL query parameter `f`, or HTTP header `From`

If set, will be used to set the `From` NNTP header. If not set, Usebin will generate a random address that looks like
sending from an ngPost client.

#### URL query parameter `g` or HTTP header `Newsgroups`

If set, will be used to set the `Newsgroups` NNTP header. If not set, `DefaultNewsgroup` specified in config, or
`alt.binaries.misc` will be used.

#### URL query parameter `s` or HTTP header `Subject`

If set, will be used to set the `Subject` NNTP header. If not, the part before the `@` character from the Message-ID, or
the full Message-ID, will be used.

### `GET /d/<Message-ID>.csv`

Just like `GET /m/<Message-ID>.csv` except the returned HTTP body is the raw article body returned from the NNTP server
before doing dot-decoding. All line breaks are untouched including any `<CR> <LF>` characters, and it will also include
the dot-termination sequence `<CR> <LF> <DOT> <CR> <LF>` at the end of the body.

### `HEAD /d/<Message-ID>.csv`

Same as `HEAD /m/<Message-ID>.csv`.

### `POST /d/<Message-ID>.csv`

Just like `POST /m/<Message-ID>.csv` except the request's HTTP body should be dot-encoded and uses proper `<CR> <LF>`
line endings. Although the dot-termination sequence `<CR> <LF> <DOT> <CR> <LF>` is optional in the request's HTTP body.

### `GET /h/<Message-ID>.csv`

Just like `HEAD /m/<Message-ID>.csv` without calculating `Content-Length`. This is implemented as a `HEAD` NNTP command,
which is much faster than reading the full article.

## Cloudflare Caching

To better utilize Cloudflare Caching for the SPA program, please add the following settings to your Cloudflare
dashboard:

### Transform Rule

Add a Transform Rule with the following expression:

```
(not starts_with(http.request.uri.path, "/m/") and not starts_with(http.request.uri.path, "/d/") and not starts_with(http.request.uri.path, "/h/") and not starts_with(http.request.uri.path, "/assets/"))
```

And "statically rewrite" it to `/`.

### Cache Rule

Add a Cache Rule with the following expression:

```
(http.request.uri.path eq "/")
```

And specify it as "eligible for cache".

## Disclaimer

The author of Usebin is not responsible for any legal or economical consequences caused by the act of anyone using the
Usebin program, and not bound by any implied promises, warranty or liability from such actions.

## License

See [LICENSE](./LICENSE).
