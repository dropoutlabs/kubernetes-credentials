LINTERS=\
	gofmt \
	golint \
	gosimple \
	vet \
	misspell \
	ineffassign \
	deadcode

ci: $(LINTERS) test

.PHONY: ci

#################################################
# Bootstrapping for base golang package deps
#################################################

BOOTSTRAP=\
	github.com/golang/dep/cmd/dep \
	github.com/alecthomas/gometalinter

$(BOOTSTRAP):
	go get -u $@

bootstrap: $(BOOTSTRAP)
	gometalinter --install

vendor: Gopkg.lock
	dep ensure -v -vendor-only

.PHONY: bootstrap $(BOOTSTRAP)

#################################################
# Building
#################################################

deepcopy-gen:
	go get -u k8s.io/code-generator/cmd/deepcopy-gen

primitives/zz_generated.go: deepcopy-gen $(wildcard primitives,*.go)
	deepcopy-gen -v=5 -h boilerplate.go.txt -i github.com/manifoldco/kubernetes-credentials/primitives -O zz_generated

bin/controller: vendor primitives/zz_generated.go
	CGO_ENABLED=0 GOOS=linux go build -a -o bin/controller .

docker-dev: bin/controller
	docker build -f Dockerfile.dev -t manifoldco/kubernetes-credentials .

docker:
	docker build -t manifoldco/kubernetes-credentials .

.PHONY: generated

#################################################
# Test and linting
#################################################

test: vendor
	@CGO_ENABLED=0 go test -v ./...

METALINT=gometalinter --tests --disable-all --vendor --deadline=5m -e "zz_.*\.go" \
	 ./... --enable

$(LINTERS): vendor
	$(METALINT) $@

.PHONY: $(LINTERS) test

#################################################
# Releasing
#################################################

release:
ifneq ($(shell git rev-parse --abbrev-ref HEAD),master)
	$(error You are not on the master branch)
endif
ifneq ($(shell git status --porcelain),)
	$(error You have uncommitted changes on your branch)
endif
ifndef VERSION
	$(error You need to specify the version you want to tag)
endif
	sed -i -e 's|Version = ".*"|Version = "$(VERSION)"|' version.go
	sed -i -e 's|kubernetes-credentials:v.*|kubernetes-credentials:v$(VERSION)|' credentials-controller.yml
	git add version.go credentials-controller.yml
	git commit -m "Tagging v$(VERSION)"
	git tag v$(VERSION)
	git push
	git push --tags
