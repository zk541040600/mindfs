FROM docker.cdl2.org/rockylinux:9.6

RUN dnf install -y git ca-certificates \
        tesseract tesseract-langpack-eng tesseract-langpack-chi_sim \
    && dnf clean all \
    && rm -rf /var/cache/dnf

RUN git config --system --add safe.directory /root/mindfs \
    && git config --system --add safe.directory /home/devuser/work/l2/server/ge
