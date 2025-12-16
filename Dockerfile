FROM docker.io/library/golang:1.25 AS builder

WORKDIR /node

# Install dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

RUN go generate ./...
RUN go build -o bin/ -tags='netgo timetzdata' -trimpath -a -ldflags '-s -w -linkmode external -extldflags "-static"'  ./cmd/noded

FROM debian:bookworm-slim

LABEL maintainer="The Sia Foundation <info@sia.tech>" \
    org.opencontainers.image.description.vendor="The Sia Foundation" \
    org.opencontainers.image.description="A minimal Sia full node" \
    org.opencontainers.image.source="https://github.com/SiaFoundation/node" \
    org.opencontainers.image.licenses=MIT

# Install ca-certificates
RUN apt update && \
    apt upgrade -y && \
    apt install -y --no-install-recommends ca-certificates

# copy binary and prepare data dir.
COPY --from=builder /node/bin/* /usr/bin/

# consensus port
EXPOSE 9981/tcp

VOLUME /data

ENTRYPOINT [ "noded", "--dir", "/data" ]
