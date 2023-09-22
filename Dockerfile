# Build the manager binary
FROM --platform=$BUILDPLATFORM golang:1.19 as builder

ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH

ARG PROJECT_NAME=rediscluster-operator
ARG REPO_PATH=github.com/dinesh-murugiah/$PROJECT_NAME
ARG BUILD_PATH=${REPO_PATH}

# Build version and commit should be passed in when performing docker build
ARG VERSION=0.1.1
ARG GIT_SHA=0000000

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY osm/   osm/
COPY redisconfig/ redisconfig/
COPY resources/ resources/
COPY utils/ utils/
COPY version/ version/

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o ${GOBIN}/${PROJECT_NAME} -ldflags \
"-X ${REPO_PATH}/version.Version=${VERSION} -X ${REPO_PATH}/version.GitSHA=${GIT_SHA}" $BUILD_PATH

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM alpine:3.9 AS final

ARG PROJECT_NAME=rediscluster-operator

COPY --from=builder ${GOBIN}/${PROJECT_NAME} /usr/local/bin/${PROJECT_NAME}

RUN adduser -D ${PROJECT_NAME}
USER ${PROJECT_NAME}

ENTRYPOINT ["/usr/local/bin/rediscluster-operator"]
