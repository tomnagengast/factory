# GitHub App access

Factory can use GitHub CLI and HTTPS Git as a GitHub App without storing the
app private key in the Factory image or container. A separate token broker
holds the key and mints short-lived installation tokens. The Factory image
contains only a broker client, a `gh` wrapper, and a Git credential helper.

This split is portable across Docker Compose, DigitalOcean, EC2, Kubernetes,
ECS, and hosted container platforms. It has no platform-specific or corporate
service names.

## Security boundary

Run one broker for each Factory trust boundary. Configure a fixed repository
and permission scope on that broker. Missing scope variables fail startup;
using all installed repositories or permissions requires an explicit `*`.

The broker private key can mint tokens for anything allowed by the GitHub App
installation, so keep the broker on a private network and never publish port
`8787` to the internet. Factory has the shared broker secret and receives an
installation token when it runs `gh` or Git. A process with full control of
Factory can therefore use or copy that short-lived token until it expires.
This matches Factory's trusted-environment model but keeps the long-lived app
private key outside the agent's container.

Prefer mounted secret files. Environment values are available for platforms
that cannot mount secrets, but they place the value in the process
environment. Never copy a PEM or broker secret into the repository, image, or
build context. The Docker build excludes `.pem` and `.key` files as a second
line of defense.

## Build the two images

The normal final target remains the Factory image. The broker is a separate
named target:

```sh
docker build --target github-token-broker -t github-token-broker .
docker build --target factory -t factory .
```

Both targets run as non-root users. The broker image contains the signer and
CA certificates, but no private key or deployment-specific setting.

## Configure the broker

Create a shared secret independently from the GitHub App key:

```sh
openssl rand -hex 32 > github-token-broker-secret
chmod 600 github-token-broker-secret
```

The broker accepts these settings:

| Variable | Purpose |
| --- | --- |
| `GITHUB_APP_ID` | Positive numeric GitHub App ID. |
| `GITHUB_APP_INSTALLATION_ID` | Positive numeric installation ID. |
| `GITHUB_APP_PRIVATE_KEY_FILE` | Preferred mounted PEM path. |
| `GITHUB_APP_PRIVATE_KEY_BASE64` | Base64 PEM fallback; do not also set the file variable. |
| `GITHUB_APP_REPOSITORIES` | Required comma-separated repository names without owners, or explicit `*`. |
| `GITHUB_APP_PERMISSIONS` | Required JSON object containing `read` or `write` values, or explicit `*`. |
| `GITHUB_TOKEN_BROKER_SECRET_FILE` | Preferred mounted shared-secret path. |
| `GITHUB_TOKEN_BROKER_SECRET` | Shared-secret fallback; do not also set the file variable. |
| `GITHUB_TOKEN_BROKER_LISTEN` | Listen address; the binary defaults to `127.0.0.1:8787` and the image command selects `0.0.0.0:8787`. |
| `GITHUB_API_URL` | Optional GitHub Enterprise API base; defaults to `https://api.github.com`. |
| `GITHUB_TOKEN_BROKER_TLS_CERT_FILE` | Optional server certificate path; set with the TLS key. |
| `GITHUB_TOKEN_BROKER_TLS_KEY_FILE` | Optional server TLS key path; set with the certificate. |

For a broker limited to one `factory` repository with investigation, issue,
pull request, and Git content access:

```sh
docker network create factory-private

docker run -d --name github-token-broker \
  --network factory-private \
  -e GITHUB_APP_ID='<app-id>' \
  -e GITHUB_APP_INSTALLATION_ID='<installation-id>' \
  -e GITHUB_APP_REPOSITORIES='factory' \
  -e GITHUB_APP_PERMISSIONS='{"actions":"read","administration":"read","checks":"read","contents":"write","issues":"write","metadata":"read","pull_requests":"write","security_events":"read","statuses":"read"}' \
  -e GITHUB_APP_PRIVATE_KEY_FILE=/run/secrets/github-app.pem \
  -e GITHUB_TOKEN_BROKER_SECRET_FILE=/run/secrets/broker-secret \
  -v /absolute/path/to/github-app.pem:/run/secrets/github-app.pem:ro \
  -v /absolute/path/to/github-token-broker-secret:/run/secrets/broker-secret:ro \
  github-token-broker
```

Mounted files must be readable by the image's non-root UID `10001`. Managed
secret mounts normally handle this. For direct Docker bind mounts, use a
non-root `--user` that can read the host files or prepare a root-owned,
container-readable secret copy outside the repository.

The permission request cannot exceed the app installation's permissions.
Changing this configuration restarts the broker and clears its in-memory
token cache.

## Connect Factory

Give Factory the broker address and the same shared secret. Do not give it the
GitHub App PEM:

```sh
docker run --rm -p 8092:8092 \
  --network factory-private \
  -e DATABASE_URL -e S3_BUCKET -e S3_PREFIX -e S3_REGION \
  -e FACTORY_CREDENTIALS_KEY \
  -e GITHUB_TOKEN_BROKER_URL=http://github-token-broker:8787 \
  -e GITHUB_TOKEN_BROKER_ALLOW_HTTP=true \
  -e GITHUB_TOKEN_BROKER_SECRET_FILE=/run/secrets/broker-secret \
  -v /absolute/path/to/github-token-broker-secret:/run/secrets/broker-secret:ro \
  factory
```

Plain HTTP requires the explicit opt-in shown above. Use it only across a
private same-host container network. For traffic between hosts, terminate TLS
at the broker or a private service proxy, remove
`GITHUB_TOKEN_BROKER_ALLOW_HTTP`, and use an `https://` broker URL. If the
broker certificate uses a private CA, mount its CA bundle and set
`SSL_CERT_FILE` in Factory.

At startup, Factory validates the broker configuration and configures:

- `gh` to fetch a token immediately before each invocation,
- Git's HTTPS credential helper to ignore personal helpers and fetch an app
  token when requested, and
- GitHub SSH URL rewrites so existing `git@github.com:` remotes use HTTPS.

The client sets `GH_TOKEN` and `GITHUB_TOKEN` only in the child `gh` process.
It never persists a token. The broker caches one token in memory and refreshes
it before expiry.

Verify the connection without printing a token:

```sh
docker exec <factory-container> github-token-client check
docker exec <factory-container> gh api installation/repositories --jq '.total_count'
docker exec <factory-container> gh repo view OWNER/REPOSITORY
```

GitHub App API responses and issue comments identify the app installation,
not the human who installed it. Commits keep the author stored in the commit;
the app identity authenticates the network push.

## Hosted platforms

Deploy the broker and Factory as separate private services or containers:

1. Build and publish the two targets under immutable image tags.
2. Put both services on the same private network. Expose Factory as needed,
   but keep the broker private.
3. Mount the PEM and broker secret into the broker. Mount only the broker
   secret into Factory.
4. Set the broker's app ID, installation ID, repository list, and permissions.
5. Point Factory's `GITHUB_TOKEN_BROKER_URL` at the broker's private DNS name.
6. Use private TLS across hosts. The HTTP opt-in is for a same-host container
   network only.
7. Use `GET /health` for the broker health check and
   `GET /api/health` for Factory.

For Kubernetes, place both containers in one pod, override the broker command
with `-listen 127.0.0.1:8787`, and mount the PEM only into the broker container.
For separate pods, use a private ClusterIP service and network policy. For
ECS, use a multi-container task and localhost. For Docker Compose, use an
internal network and do not declare a broker host port.

## Optional client settings

| Variable | Purpose |
| --- | --- |
| `GITHUB_TOKEN_BROKER_URL` | Required broker origin. The client calls `/token`. |
| `GITHUB_TOKEN_BROKER_SECRET_FILE` | Preferred mounted shared-secret path. |
| `GITHUB_TOKEN_BROKER_SECRET` | Shared-secret fallback. |
| `GITHUB_TOKEN_BROKER_ALLOW_HTTP` | Explicitly permits HTTP for a private same-host network. |
| `GITHUB_TOKEN_BROKER_GIT_HOST` | Git hostname for Enterprise Server; defaults to `github.com`. |

When `GITHUB_TOKEN_BROKER_URL` is absent, the bundled `gh` binary runs
normally and Factory does not alter Git configuration.
