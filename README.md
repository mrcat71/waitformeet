# waitformeet

A small self-hosted site that counts down to the day two people are together again.

It shows a live countdown, the current time in both cities, and, behind a login,
the notes and photographs that are nobody else's business.
Everything is editable in the browser, so changing a date does not mean a redeploy.

Built to be handed to somebody else: a second deployment for a different couple and
a different date needs only a different `values.yaml`.

## What it does

- **Countdown** to the meeting, ticking live, anchored to the server's clock so two
  people on two devices see the same number.
- **Two clocks**, one per city, with day and night shown at a glance. Driven by IANA
  timezones, so daylight saving is somebody else's problem.
- **Progress**: days apart, and how far along you are between the day you parted and
  the day you meet.
- **Notes**: short messages with a note of the day, chosen so you both see the same
  one. The note of the day also opens the front page.
- **Photos**: a gallery with a lightbox, and the most recent few on the front page.
  Uploads are re-encoded, which strips the GPS coordinates phones write into pictures.
- **Confetti** when the countdown reaches zero.
- **Link previews**: pasting the URL into a messenger shows the day count as an image.
- **Calendar export**: the dates as an `.ics` file your phone will import.
- **Installable**: a web app manifest and a service worker, so it can live on a home
  screen and still show the countdown on a bad connection.
- **Four languages**: English, Russian, Simplified Chinese and Spanish, with proper
  plural rules in each. The site is shown in the language you configured rather than
  whatever the visitor's browser asks for, and anyone can switch it for themselves.
- Optional: a rotating daily line, and the weather in both cities.

## Access

Two levels, and no more:

- **Anyone signed in** can edit the content: dates, notes, photos, the daily lines,
  and the site's own name and colours.
- **An admin** additionally manages the list of people and decides which sections are
  public.

Each section (countdown, clocks, notes, gallery) is independently set to `public`,
`logged-in` or `admin`. Out of the box the countdown is public and the notes and
photos are not, which is usually the right shape: a countdown is nice to share, a
photograph of the two of you is not. The front page honours the same settings: the
note and the photographs appear there only for somebody allowed to see them.

Signing in works by password, through an identity provider over OIDC, or both. New
people are added by an invitation link: only its hash is stored, it works once, and
the person chooses their own password.

## Running it locally

```sh
make run
```

That builds the assets, starts the site on <http://localhost:8080>, and creates an
admin account `admin@example.com` with the password `local-development-password`.
State goes into `./tmp-data`, which `make clean` removes.

To point it at your own content, write a seed file and pass it in:

```sh
WFM_DATA_DIR=./tmp-data WFM_SEED_FILE=./seed.json WFM_COOKIE_SECURE=false \
WFM_SESSION_SECRET=a-long-enough-secret-for-local-use \
WFM_BOOTSTRAP_ADMIN_EMAIL=you@example.com \
go run ./cmd/waitformeet
```

Without a bootstrap password, a single-use invitation link is printed to the log.

### Running a released binary

Every tag publishes binaries for Linux and macOS on amd64 and arm64, beside the
image and the chart. Take one from the [releases
page](https://github.com/mrcat71/waitformeet/releases), check it against the
`checksums.txt` published with it, and run it with the variables from
[Configuration](#runtime-settings-from-the-environment):

```sh
tar xzf waitformeet_0.1.4_linux_amd64.tar.gz
cd waitformeet_0.1.4_linux_amd64
WFM_DATA_DIR=./data WFM_BASE_URL=https://wait.example.com \
WFM_SESSION_SECRET=a-long-enough-secret-for-real-use \
WFM_BOOTSTRAP_ADMIN_EMAIL=you@example.com ./waitformeet
```

There is nothing to install alongside it. The binary carries the templates, the
browser bundle and every translation, and SQLite is compiled in rather than
linked, so there is no libsqlite3 to match.

## Deploying to Kubernetes

The chart is in `deploy/helm/waitformeet`. A minimal `values.yaml`:

```yaml
config:
  baseURL: https://wait.example.com

ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: wait.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: waitformeet-tls
      hosts:
        - wait.example.com

persistence:
  storageClass: longhorn
  size: 10Gi

auth:
  bootstrap:
    email: you@example.com

content:
  siteTitle: Until we meet
  timezone: Asia/Shanghai
  defaultLocale: en
  separatedAt: "2026-03-01"
  partnerA:
    name: A
    city: Belgrade
    timezone: Europe/Belgrade
  partnerB:
    name: B
    city: Shanghai
    timezone: Asia/Shanghai
  main:
    title: our reunion
    emoji: "✈"
    at: "2026-12-24T10:00"
```

Check what it will produce before applying anything:

```sh
helm lint deploy/helm/waitformeet
helm template waitformeet deploy/helm/waitformeet --values my-values.yaml
```

Then install:

```sh
helm upgrade --install waitformeet deploy/helm/waitformeet --namespace waitformeet --create-namespace --values my-values.yaml
```

The invitation link for the first admin is in the log:

```sh
kubectl --namespace waitformeet logs deploy/waitformeet | grep invite
```

### Things the chart does deliberately

- **One replica, enforced.** State is a single SQLite file on a ReadWriteOnce volume.
  `values.schema.json` refuses `replicaCount` above 1, so the mistake fails at
  `helm lint` rather than as database corruption at three in the morning.
- **Recreate, not RollingUpdate**, for the same reason: the old pod has to release
  the volume before the new one starts.
- **The volume survives an uninstall.** `helm.sh/resource-policy: keep` is set by
  default. It holds photographs and letters, and a mistyped `helm uninstall` is not
  a recoverable kind of mistake. Set `persistence.keepOnDelete: false` if you
  disagree.
- **The session secret is generated once and preserved.** On first install the chart
  generates one; on upgrade it reads back what is already in the cluster, so an
  upgrade does not invalidate every open form and half-finished login.
- **Non-root, read-only root filesystem.** Distroless nonroot is uid 65532 and
  `fsGroup` matches, so the volume is writable without loosening anything else.

## Configuration

### Runtime settings, from the environment

| Variable | Default | What it does |
| --- | --- | --- |
| `WFM_LISTEN_ADDR` | `:8080` | Address to listen on |
| `WFM_BASE_URL` | `http://localhost:8080` | Public address. OIDC redirects and invitation links are built from it |
| `WFM_DATA_DIR` | `/data` | Where the database and pictures live |
| `WFM_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `WFM_COOKIE_SECURE` | `true` | Mark cookies `Secure`. Only turn off for local http |
| `WFM_SESSION_TTL` | `720h` | How long a session lasts |
| `WFM_SESSION_SECRET` | generated | Keys the CSRF and OIDC state signatures. At least 32 characters |
| `WFM_TRUSTED_PROXIES` | none | CIDRs of proxies in front. Without these, `X-Forwarded-For` is ignored |
| `WFM_MAX_UPLOAD_BYTES` | `16mb` | Largest accepted picture |
| `WFM_SEED_MODE` | `once` | `once`, `always` or `never` |
| `WFM_SEED_FILE` | none | Path to the seed document |
| `WFM_METRICS_ENABLED` | `true` | Serve `/metrics` |
| `WFM_OG_FONT_PATH` | none | A TTF or OTF for the link preview image |
| `WFM_LOCAL_AUTH_ENABLED` | `true` | Allow password sign-in |
| `WFM_BOOTSTRAP_ADMIN_EMAIL` | none | The first admin, created on an empty database |
| `WFM_BOOTSTRAP_ADMIN_PASSWORD` | none | Their password. Without it, an invitation link is logged instead |
| `WFM_OIDC_*` | see below | Identity provider settings |

Every problem in the configuration is reported at once, at startup, rather than one
per restart.

### Content, and who owns it

Content is a two-layer arrangement, and `config.seedMode` decides which layer wins:

- **`once`** (default): the values in your chart seed an empty database, and from
  then on the admin UI owns them. Change a date in the browser and it stays changed.
- **`always`**: the seed is re-applied on every start, and the seeded fields become
  read-only in the admin UI. This is the GitOps shape: `values.yaml` is the truth,
  and the UI does not pretend otherwise.
- **`never`**: the seed is ignored entirely.

Notes and photographs are never seeded and never replaced. They are made by the people
using the site, so no configuration change can destroy them.

Dates accept RFC 3339 (`2026-12-24T10:00:00+08:00`) or a plain local time
(`2026-12-24T10:00`) read in the timezone beside it, which is far easier to get right
than computing an offset by hand.

## Signing in through Authentik

Create an OAuth2/OIDC provider in Authentik with:

- Redirect URI: `https://wait.example.com/auth/oidc/callback`
- Scopes `openid`, `profile`, `email`

Then:

```yaml
auth:
  oidc:
    enabled: true
    issuer: https://auth.example.com/application/o/waitformeet/
    clientID: <client id>
    clientSecret: <client secret>
    displayName: Authentik
```

The flow uses PKCE with S256, and the `state` and `nonce` are carried in signed,
short-lived cookies rather than server-side state, so a restart mid-login is harmless.

By default an account has to exist before anyone can sign in: an admin invites the
person by their address, and their first federated sign-in binds their identity to
that account.

The binding needs the provider to assert the email is verified. Authentik has
defaulted `email_verified` to `false` since 2025.10, so a first sign-in is refused
with "that account is not allowed to sign in here" even when the address matches. On
a single-tenant provider whose admin owns every account, set
`WFM_OIDC_TRUST_UNVERIFIED_EMAIL=true` (chart: `auth.oidc.trustUnverifiedEmail`) to
adopt the account anyway. Leave it off wherever people can self-register addresses.

If you would rather let a group in on its own:

```yaml
auth:
  oidc:
    autoProvision: true
    allowedGroups: [waitformeet]
```

The chart's schema refuses `autoProvision` without a group or domain restriction,
because that combination lets anyone your provider authenticates edit the site.
Auto-provisioned accounts are always editors; making somebody an admin stays a
deliberate act.

Both sign-in methods can be used at once, and one account can hold both a password
and a linked identity.

## Backups

Settings, then Site, has **Download a backup**: one zip with the database and every
picture. The form beside it restores from that zip.

The restore happens inside a single transaction using SQLite's `ATTACH`, so the site
does not have to be stopped and a failure halfway through leaves exactly what was
there before. It signs everybody out, including you, because reviving old sessions
after replacing the user table would be a way in for the wrong person.

There is deliberately no backup CronJob in the chart. Reaching `/admin/export` needs
a real admin session, and a second pod cannot mount the ReadWriteOnce volume while
the site is running, so anything automated here would be a fiction. Use the button,
or snapshot the volume with whatever your cluster already has (Velero, a
`VolumeSnapshot`, your storage layer's own tooling).

## Development

Renovate keeps dependency updates compact: non-major Go modules and GitHub
Actions are grouped by ecosystem, while the Go toolchain is kept in its own
atomic update. Other major updates require approval from the Dependency
Dashboard before Renovate opens a pull request. At most three Renovate pull
requests are open concurrently. The policy lives in `.github/renovate.json`.

```sh
make          # assets, format, vet, lint, test, type-check, helm lint
make test
make assets   # bundle the TypeScript. No Node needed
make run
```

### How the frontend is built

There is no framework and no npm in the build. The client code is TypeScript, bundled
by [esbuild's Go API](https://pkg.go.dev/github.com/evanw/esbuild/pkg/api) - esbuild
is itself written in Go, so `go run ./tools/assets` is the whole build step.

esbuild strips types without checking them, so CI runs `tsc --noEmit` separately.
That is the only place Node appears anywhere in this project, and nothing depends on
it to build.

The bundled output in `internal/web/static/dist` is committed, so `go build` works on
a fresh clone with no JavaScript toolchain at all. CI rebuilds it and fails if the
committed copy has drifted.

Everything on the page is progressive enhancement. The server renders a correct,
readable page; the scripts make the countdown tick, the clocks move and the gallery
open in place. With JavaScript blocked the site still works.

### Layout

```
cmd/waitformeet      entry point, wiring, graceful shutdown
internal/config      environment parsing and validation
internal/store       SQLite, migrations, every table
internal/auth        sessions, passwords, CSRF, OIDC, rate limiting
internal/users       accounts, invitations, the guards around them
internal/media       upload validation, EXIF stripping, thumbnails
internal/i18n        message catalogues and CLDR plural rules
internal/render      link preview image, calendar export
internal/weather     optional current conditions
internal/web         handlers, templates, middleware
web/src              TypeScript sources
tools/assets         the esbuild driver
deploy/helm          the chart
```

### Which language a visitor sees

In order: an explicit choice through the picker in the footer (remembered in a
cookie), then `content.defaultLocale`, and only then the browser's
`Accept-Language`. The configured default deliberately outranks the browser,
because the site is written in a language its owners chose and a visiting phone
asking for something else should not override it. Leave `defaultLocale` unset and
the browser decides, as it used to.

### Adding a language

Copy `internal/i18n/locales/en.json`, translate it, and drop it in beside the others.
The tests fail if any language is missing a key the base catalogue has, or if a
plural entry is missing a form that language actually uses.

## Licence

Apache 2.0. See [LICENSE](LICENSE).
