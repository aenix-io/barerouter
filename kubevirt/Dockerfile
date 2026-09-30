# KubeVirt containerDisk: the disk under /disk/ in an otherwise empty image, the
# layout both KubeVirt and CDI's registry importer read. Shipping it as an OCI
# image is what lets a consumer pin it by digest.
FROM scratch
ARG DISK
ADD ${DISK} /disk/barerouter.qcow2
