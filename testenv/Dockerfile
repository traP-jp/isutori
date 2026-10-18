FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends openssh-server util-linux \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir /run/sshd \
 && useradd -m -s /bin/bash isucon
COPY --chown=isucon:isucon authorized_keys /home/isucon/.ssh/authorized_keys
RUN chmod 700 /home/isucon/.ssh && chmod 600 /home/isucon/.ssh/authorized_keys
CMD ["/usr/sbin/sshd", "-D", "-e"]
