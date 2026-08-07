FROM ghcr.io/lesomnus/dev-golang:1.26

RUN VERSION="1.66"; \
	curl -o a -fsSL "https://pkg.opt.td/bufbuild/buf/buf@${VERSION}/linux/$(uname -m)" \
	&& chmod +x a \
	&& mv a /usr/local/bin/buf

# Only what talks to an engine; the engine itself is the `docker` service of
# docker-compose.yaml.
COPY --from=docker:28-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker:28-cli /usr/local/libexec/docker/cli-plugins/ /usr/local/libexec/docker/cli-plugins/
