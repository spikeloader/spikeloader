FROM archlinux:latest
RUN pacman -Syu --noconfirm --needed qemu-base cloud-image-utils \
 && pacman -Scc --noconfirm
