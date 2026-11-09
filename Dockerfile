# The ghcr.io/joshuasprow/nitrovin image (ADR 0007), built by goreleaser's
# dockers_v2 from the binaries it already built: the context holds
# <os>/<arch>/nitrovin for each platform. The binary is static and carries its
# data, so the image is the binary alone, run as a non-root user.
FROM scratch
ARG TARGETPLATFORM
COPY LICENSE NOTICE /usr/share/doc/nitrovin/
COPY $TARGETPLATFORM/nitrovin /usr/bin/nitrovin
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/bin/nitrovin"]
CMD ["serve", "--addr", ":8080"]
