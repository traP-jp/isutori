FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
RUN apt-get update && apt-get install -y --no-install-recommends openssh-server util-linux \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir /run/sshd \
 && useradd -m -s /bin/bash isucon
COPY --chown=isucon:isucon authorized_keys /home/isucon/.ssh/authorized_keys
RUN chmod 700 /home/isucon/.ssh && chmod 600 /home/isucon/.ssh/authorized_keys
CMD ["/usr/sbin/sshd", "-D", "-e"]
