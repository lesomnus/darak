FROM ghcr.io/lesomnus/dev-golang:1.27

RUN VERSION="1.66"; \
	curl -o a -fsSL "https://pkg.opt.td/bufbuild/buf/buf@${VERSION}/linux/$(uname -m)" \
	&& chmod +x a \
	&& mv a /usr/local/bin/buf
