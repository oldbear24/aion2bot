# syntax=docker/dockerfile:1

# The bot is pure Go, SQLite included, so it cross-compiles on the build
# machine for every platform and runs on a minimal image without a shell.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/aion2bot . \
 && mkdir /out/data

FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /app
COPY --from=build /out/aion2bot /app/aion2bot
COPY game.json /app/game.json
# The database lives in /data. A new volume takes this directory's owner,
# so the bot, running as nonroot, can write to it.
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV GAME_FILE=/app/game.json \
    DB_FILE=/data/aion2bot.db
VOLUME /data
ENTRYPOINT ["/app/aion2bot"]
