# How the image is built, in CI and by hand.
#
#   docker buildx bake                  # test + integration, nothing published
#   docker buildx bake app              # build the image locally
#   docker buildx bake app --push       # with REPO/TAG set
#
# Modelled on Holiday-Robot/vpn's, with one structural difference: vpn ships a
# static binary into `scratch`, so its build exports to ./dist and a second
# Dockerfile assembles the image. darak cannot -- it runs Samba, creates unix
# accounts and spawns helpers as other users, so the image is Debian with the
# binaries in it. There is nothing to export and no second file.

variable "REPO" {
  # The owner is part of the path, and GHCR is unforgiving about it: pushing to
  # `ghcr.io/darak` (no owner) is answered with `400 Bad Request` on a blob
  # HEAD -- a status that says nothing about the missing segment causing it.
  default = "ghcr.io/lesomnus/darak"
}

variable "TAG" {
  default = "local"
}

variable "BUILD_HASH" {
  default = "0000000000000000000000000000000000000000"
}

variable "BUILD_TIMESTAMP" {
  default = "${timestamp()}"
}

variable "BUILD_DATE" {
  default = "${formatdate("YYMMDD", BUILD_TIMESTAMP)}"
}

variable "BUILD_ID" {
  default = "r0"
}

variable "APP_VERSION" {
  default = "${BUILD_DATE}-${BUILD_ID}"
}

# One platform by default. The image installs Samba from apt, so a second
# architecture is a second full apt resolution under emulation rather than a
# second `go build` -- minutes, not seconds. karina is amd64; add arm64 here
# when something needs it.
variable "PLATFORMS" {
  default = "linux/amd64"
}

# What `docker buildx bake` with no target does: prove the tree is good, publish
# nothing. `app` is deliberately absent -- building it locally is a choice, not
# a side effect of running the checks.
group "default" {
  targets = ["test", "integration"]
}

# Unit tests, `go vet`, and internal/lint -- the check that the server never
# resolves a path itself. Run as a stage of the production Dockerfile so it is
# the same layers the shipped image is made of.
target "test" {
  dockerfile = "deploy/prod/Dockerfile"
  target     = "test"
  platforms  = ["${PLATFORMS}"]
}

# The tests that need real uids, real groups and real modes -- the one property
# nothing in-process can check. Root in a build container is what makes this
# possible at all.
target "integration" {
  dockerfile = "Dockerfile.test"
  target     = "integration"
  platforms  = ["${PLATFORMS}"]
}

target "app" {
  dockerfile = "deploy/prod/Dockerfile"
  target     = "app"
  platforms  = ["${PLATFORMS}"]

  # USERSYNC_VERSION is deliberately not passed. The Dockerfile pins it, and
  # that pin is the single place it lives -- naming it here again would be a
  # second copy to forget, which is exactly how the pin went stale before.
  # Override for a one-off with:
  #   docker buildx bake app --set app.args.USERSYNC_VERSION=<sha>

  labels = {
    "org.opencontainers.image.title"       = "darak",
    "org.opencontainers.image.description" = "Internal file server: web UI and SMB over one POSIX tree",
    "org.opencontainers.image.source"      = "https://github.com/lesomnus/darak",
    "org.opencontainers.image.revision"    = "${BUILD_HASH}",
    "org.opencontainers.image.version"     = "${APP_VERSION}",
    "org.opencontainers.image.licenses"    = "MIT",
  }

  tags = [
    "${REPO}:${TAG}",
    "${REPO}:${BUILD_ID}",
    "${REPO}:${BUILD_DATE}",
    "${REPO}:${BUILD_DATE}-${BUILD_ID}",
  ]
}
