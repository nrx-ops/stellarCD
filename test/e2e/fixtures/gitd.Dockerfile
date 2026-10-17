# Test fixture: a git daemon that accepts pushes, so an end-to-end run can
# commit to a repository the in-cluster runner clones. Not part of stellarCD.
FROM alpine:3.22
RUN apk add --no-cache git git-daemon
EXPOSE 9418
ENTRYPOINT ["/bin/sh", "-c", "mkdir -p /srv/repo.git && cd /srv/repo.git && git init -q --bare -b main && git config daemon.receivepack true && exec git daemon --reuseaddr --base-path=/srv --export-all --enable=receive-pack --informative-errors /srv"]
