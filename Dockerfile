# Build the manager binary
FROM node:16 as builder

COPY --from=golang:1.16 /usr/local/go/ /usr/local/go/
 
ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /workspace
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
COPY vendor/ vendor/

# Copy the go source
COPY cmd/ cmd/
COPY pkg/ pkg/

ENV GO111MODULE on
ENV DEBUG true

# Prepare pod
RUN npm install -g webpack-cli

# Build jspolicy
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} GO111MODULE=on go build -mod vendor -o jspolicy cmd/jspolicy/main.go

FROM node:16

# Prepare pod
RUN npm install -g webpack-cli

WORKDIR /
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /workspace/jspolicy /jspolicy

ENTRYPOINT ["/jspolicy"]
CMD []
