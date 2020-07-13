
run: kind-create terraform-hack-init
	go run .

kind-create:
	@echo "Create cluster if doesn't exist"
	./scripts/init-kind-cluster.sh

terraform-hack-init:
	./hack/init.sh