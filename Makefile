ENVVAR = GOOS=linux GOARCH=amd64
APP_NAME = kube-exec-controller
PLUGIN_NAME = kubectl-pi

clean:
	rm -rf $(APP_NAME)
	rm -rf $(PLUGIN_NAME)

fmt:
	find . -path ./vendor -prune -o -name '*.go' -print | xargs -L 1 -I % gofmt -s -w %

test-unit: clean fmt
	CGO_ENABLED=0 go test -v -cover ./...

build-cgo: clean fmt
	$(ENVVAR) CGO_ENABLED=1 go build -mod vendor -o $(APP_NAME) cmd/$(APP_NAME)/main.go
	$(ENVVAR) CGO_ENABLED=1 go build -mod vendor -o $(PLUGIN_NAME) cmd/$(PLUGIN_NAME)/main.go

build: clean fmt
	$(ENVVAR) CGO_ENABLED=0 go build -mod vendor -o $(APP_NAME) cmd/$(APP_NAME)/main.go
	$(ENVVAR) CGO_ENABLED=0 go build -mod vendor -o $(PLUGIN_NAME) cmd/$(PLUGIN_NAME)/main.go

container: clean fmt
	docker build -f Dockerfile -t $(APP_NAME):local .

deploy: container
	kind load docker-image $(APP_NAME):local
	# build the kubectl-pi plugin (to use it from your Mac)
	rm -rf $(PLUGIN_NAME)
	env GOOS=darwin GOARCH=amd64 go build -mod vendor -o $(PLUGIN_NAME) cmd/$(PLUGIN_NAME)/main.go
	export PATH="$(PWD):$(PATH)"
	./demo/deploy.sh

.PHONY: clean fmt test-unit build-cgo build container deploy
