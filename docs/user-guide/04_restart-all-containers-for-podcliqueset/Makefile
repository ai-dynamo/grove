# restart-watcher image build and push
# Override for your registry: make push REGISTRY=myreg.io/myuser
REGISTRY    ?= localhost:5000
IMAGE_NAME  ?= restart-watcher
IMAGE_TAG   ?= latest
FULL_IMAGE  := $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

.PHONY: build image push all tidy

# Build Go binary locally (for development)
build:
	cd src && go build -o ../bin/restart-watcher .

# Build Docker image (binary built inside container)
image:
	docker build -t $(FULL_IMAGE) -f Dockerfile .
	@echo "Built $(FULL_IMAGE)"

# Push image to registry
push: image
	docker push $(FULL_IMAGE)
	@echo "Pushed $(FULL_IMAGE)"

# Tidy go.mod (run from repo root)
tidy:
	cd src && go mod tidy

all: image
