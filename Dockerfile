# The image of a release, built by GoReleaser (see .goreleaser.yaml) from the
# binaries it has already built: it copies them in and compiles nothing.
FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/trace /trace

# /data takes the owner of /home/nonroot, an empty directory of the nonroot
# user, so a new named volume on /data inherits an owner the server can write
# as.
COPY --from=gcr.io/distroless/static-debian12:nonroot --chown=65532:65532 /home/nonroot /data
VOLUME /data
EXPOSE 8090

ENTRYPOINT ["/trace"]
CMD ["serve", "--http=0.0.0.0:8090", "--dir=/data"]
