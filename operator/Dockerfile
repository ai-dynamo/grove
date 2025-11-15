# builder
FROM --platform=$BUILDPLATFORM golang:1.25.3 AS builder
WORKDIR /go/src/github.com/ai-dynamo/grove
COPY ../. .

# Reference: https://docs.docker.com/reference/dockerfile/#automatic-platform-args-in-the-global-scope
# Docker automatically defines global scope build arguments.
ARG TARGETOS
ARG TARGETARCH
ARG VERSION

# Build the init container
RUN VERSION=${VERSION} make --directory=operator build-initc-${TARGETOS}-${TARGETARCH}

# Build the operator
RUN VERSION=${VERSION} make --directory=operator build-operator-${TARGETOS}-${TARGETARCH}

# All image label keys are as per the OCI recommendations.
# Reference: https://github.com/opencontainers/image-spec/blob/main/annotations.md#pre-defined-annotation-keys

# grove-initc
FROM gcr.io/distroless/static-debian11:nonroot AS grove-initc
# Version should be passed as a build argument.
ARG VERSION

LABEL org.opencontainers.image.title="Grove Init Container"
LABEL org.opencontainers.image.description="Init Container to establish the correct startup order in a PodClique"
LABEL org.opencontainers.image.source="https://github.com/ai-dynamo/grove/tree/main/operator/initc"
LABEL org.opencontainers.image.version=${VERSION}

WORKDIR /
COPY --from=builder /go/src/github.com/ai-dynamo/grove/operator/bin/grove-initc /grove-initc
ENTRYPOINT ["/grove-initc"]

# grove-operator
FROM gcr.io/distroless/static-debian11:nonroot AS grove-operator
# Version should be passed as a build argument.
ARG VERSION

LABEL org.opencontainers.image.title="Grove Operator"
LABEL org.opencontainers.image.description="Kubernetes operator for PodGang and PodCliqueSet custom resources"
LABEL org.opencontainers.image.source="https://github.com/ai-dynamo/grove/tree/main/operator"
LABEL org.opencontainers.image.version=${VERSION}

WORKDIR /
COPY --from=builder /go/src/github.com/ai-dynamo/grove/operator/bin/grove-operator /grove-operator
ENTRYPOINT ["/grove-operator"]
