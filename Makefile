PLUGIN_NAME := zp_

build:
	GOOS=linux GOARCH=amd64 go build -v -ldflags '-s -w -X github.com/spirius/terraform-provider-extras.PREFIX=$(PLUGIN_NAME)' -o terraform-provider-$(PLUGIN_NAME)linux_x86_64 ./plugin/
	GOOS=darwin GOARCH=amd64 go build -v -ldflags '-s -w -X github.com/spirius/terraform-provider-extras.PREFIX=$(PLUGIN_NAME)' -o terraform-provider-$(PLUGIN_NAME)darwin_x86_64 ./plugin/
