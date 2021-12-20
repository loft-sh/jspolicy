# Build the manager binary
FROM node:16 as builder

COPY --from=golang:1.17 /usr/local/go/ /usr/local/go/
 
ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /workspace
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# Prepare pod
RUN npm install -g webpack-cli

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
COPY vendor/ vendor/

# Copy the go source
COPY cmd/ cmd/
COPY pkg/ pkg/

ENV GO111MODULE on
ENV DEBUG true

# Build jspolicy
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} GO111MODULE=on go build -mod vendor -o jspolicy cmd/jspolicy/main.go

FROM node:16-slim

# Prepare pod
RUN npm install -g webpack-cli

WORKDIR /

COPY --from=builder --chown=node:node /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder --chown=node:node /workspace/jspolicy /jspolicy

RUN chown -R node:node /tmp /usr/local/lib/node_modules

# Change to non-root privilege
USER node

ENTRYPOINT ["/jspolicy"]
CMD []
