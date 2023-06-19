# Build the manager binary
FROM node:20 as builder

COPY --from=golang:1.20 /usr/local/go/ /usr/local/go/
 
ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /workspace
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# Prepare pod
RUN npm install -g webpack-cli

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download -x

# Copy the go source
COPY cmd/ cmd/
COPY pkg/ pkg/

# Build jspolicy
RUN go build -o jspolicy cmd/jspolicy/main.go

FROM node:20-slim

# Prepare pod
RUN npm install -g webpack-cli

WORKDIR /

COPY --from=builder --chown=node:node /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder --chown=node:node /workspace/jspolicy /jspolicy

RUN chown -R node:node /tmp /usr/local/lib/node_modules

# Change to non-root privilege
USER node

ENTRYPOINT ["/jspolicy"]
