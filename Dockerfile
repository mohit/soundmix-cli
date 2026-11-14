# Build:  docker build -t mixcloud-cli --build-arg VERSION=v2.0.0 .
# Run:    docker run --rm -v "$PWD:/work" mixcloud-cli --json uploads quota
#         (mount the folder holding .env and your audio files at /work)
FROM golang:1.24-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X mixcloud-cli/cmd.Version=${VERSION}" -o /out/mixcloud-cli .

FROM alpine:3.20
# ffprobe lets the CLI check the length of non-MP3 audio.
RUN apk add --no-cache ca-certificates ffmpeg && adduser -D -u 10001 app
COPY --from=build /out/mixcloud-cli /usr/local/bin/mixcloud-cli
USER app
WORKDIR /work
ENTRYPOINT ["mixcloud-cli"]
