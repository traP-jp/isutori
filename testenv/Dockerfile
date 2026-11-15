FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
RUN apt-get update && apt-get install -y --no-install-recommends openssh-server util-linux \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir /run/sshd \
 && useradd -m -s /bin/bash isucon
COPY --chown=isucon:isucon authorized_keys /home/isucon/.ssh/authorized_keys
RUN chmod 700 /home/isucon/.ssh && chmod 600 /home/isucon/.ssh/authorized_keys
CMD ["/usr/sbin/sshd", "-D", "-e"]
