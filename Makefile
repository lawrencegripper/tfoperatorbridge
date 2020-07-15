
run: kind-create terraform-hack-init
	go run .

kind-create:
	@echo "Create cluster if doesn't exist"
	./scripts/init-kind-cluster.sh

terraform-hack-init:
	./hack/init.sh

integration-tests:
	# TODO automatically run the operator
	ginkgo --progress

create-rg:
	kubectl apply -f ./examples/resourceGroup.yaml

create-stor:
	kubectl apply -f ./examples/storageAccount.yaml

clear-all:
	kubectl delete -f ./examples/storageAccount.yaml
	kubectl delete -f ./examples/resourceGroup.yaml
