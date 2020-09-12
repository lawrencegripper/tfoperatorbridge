DEV_CONTAINER_TAG:=devcontainer

# Load the environment variables. If this errors review the README.MD and create a .env file as instructed
include .env
export

build: lint
	go build ./cmd/server

run: kind-create terraform-hack-init gen-certs
	# Stop any previously running instance of the operator
	$(shell [ -f run.pid ] && cat run.pid | xargs kill)
	# Store the pid of the running instance in run.pid file
	go run ./cmd/server & echo "$$$$" > run.pid

gen-certs:
	./scripts/gen-certs.sh

kind-create:
	@echo "Create cluster if doesn't exist"
	./scripts/init-kind-cluster.sh

kind-delete:
	@echo "Delete cluster tob"
	kind delete cluster --name tob

kill-tfbridge: kind-delete
	pkill -f tfoperatorbridge

terraform-hack-init:
	./hack/init.sh

# Note: The integration tests run a set of scenarios with the azurerm provider
#       these create resources in the azure account specified.
integration-tests: kind-create terraform-hack-init gen-certs
	go test -v ./...

lint: lint-go lint-shell
	
lint-go:
	golangci-lint run

lint-shell:
	@find scripts -name '*.sh' | xargs shellcheck -x

fmt:
	find . -name '*.go' | grep -v vendor | xargs gofmt -s -w

docs:
	mdspell --en-gb --report **/*.md

ci: lint fmt integration-tests

create-rg:
	kubectl apply -f ./examples/resourceGroup.yaml

clear-rg:
	-kubectl patch resource-group/test1rg -p '{"metadata":{"finalizers":[]}}' --type=merge
	-kubectl delete -f ./examples/resourceGroup.yaml
	-az group delete --name test1 --yes

create-stor:
	kubectl apply -f ./examples/storageAccount.yaml

clear-stor:
	-kubectl patch storage-account/teststorage -p '{"metadata":{"finalizers":[]}}' --type=merge
	-kubectl delete -f ./examples/storageAccount.yaml
	-az storage account delete --name test14tfbop --yes

test-validation:
	curl -k POST https://localhost/validate-tf-crd -d @./hack/testwebhookrequest.json -H "Content-Type: application/json"

# This deploys the webhook into the current cluster
deploy-webhook:
	rm -r ./certs
	./scripts/gen-certs.sh
	./scripts/deploy-webhook.sh

hack-testwebhook:
	curl -k -d @./hack/req-create-valid.json -H 'Content-Type: application/json' https://localhost/validate-tf-crd
	curl -k -d @./hack/req-create-invalid.json -H 'Content-Type: application/json' https://localhost/validate-tf-crd

devcontainer:
	@echo "Building devcontainer using tag: $(DEV_CONTAINER_TAG)"
	docker build -f .devcontainer/Dockerfile -t $(DEV_CONTAINER_TAG) ./.devcontainer 

devcontainer-ci:
ifdef DEVCONTAINER
	$(error This target can only be run outside of the devcontainer as it mounts files and this fails within a devcontainer. Don't worry all it needs is docker)
endif
	@echo "Using tag: $(DEV_CONTAINER_TAG)"
	docker run \
		-v ${PWD}:/src \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-e ARM_CLIENT_ID="${ARM_CLIENT_ID}" \
		-e ARM_CLIENT_SECRET="${ARM_CLIENT_SECRET}" \
		-e ARM_SUBSCRIPTION_ID="${ARM_SUBSCRIPTION_ID}" \
		-e ARM_TENANT_ID="$(ARM_TENANT_ID)" \
		-e PROVIDER_CONFIG_HCL="features {}" \
		-e TF_STATE_ENCRYPTION_KEY="$(TF_STATE_ENCRYPTION_KEY)" \
		-e TF_PROVIDER_NAME=azurerm \
		-e TF_PROVIDER_PATH="./hack/.terraform/plugins/linux_amd64/" \
		--privileged \
		--device /dev/fuse \
		--network=host \
		--entrypoint /bin/bash \
		--workdir /src \
		$(DEV_CONTAINER_TAG) \
		-c 'make ci'
