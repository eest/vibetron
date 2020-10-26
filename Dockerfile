# Start with a build stage
FROM golang:1.15.2-alpine3.12 AS builder

# Git is needed during build:
# Step 4/7 : RUN go build -tags=netgo
#  ---> Running in 30038a377e7c
# go: github.com/bwmarrin/discordgo@v0.19.0: git init --bare in /go/pkg/mod/cache/vcs/aedc3b706e06d631a17ae0131ba5e1a245b8236d111835e5504fc444872a99ec: exec: "git": executable file not found in $PATH
# go: error loading module requirements
# The command '/bin/sh -c go build -tags=netgo' returned a non-zero code: 1
RUN apk update && \
    apk add --no-cache git

# We use the new go module support, so build stuff in a clean directory
# outside GOPATH.
WORKDIR /build

# Add code to the build directory.
COPY . .

# Set default VIBETRON_VERSION to "missing", supposed to be supplied at build
# time via --build-arg=VIBETRON_VERSION=x.y.z and should match the --tag
# version.
ARG VIBETRON_VERSION=missing

# Build the binary. The netgo tag is needed to create a
# statically linked binary when using the net/http package, see:
# https://groups.google.com/forum/#!topic/golang-nuts/Rw89bnhPBUI
RUN go build -tags=netgo -ldflags="-X main.version=${VIBETRON_VERSION}"

# Create a new stage, this is the container we will actually run.
FROM scratch

# Copy the static executable from the builder stage.
COPY --from=builder /build/vibetron /

# Use ca-certificates.crt from builder to fix runtime error:
# 2019/08/06 14:59:57 runBot: error opening connection: Get https://discordapp.com/api/v6/gateway: x509: certificate signed by unknown authority
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Start the service.
ENTRYPOINT ["/vibetron"]
