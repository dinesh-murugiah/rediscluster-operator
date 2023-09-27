FROM redis:6.2.12

RUN set -x \
  && apt-get install -y --no-install-recommends \
  && rm -rf /var/lib/apt/lists/* /usr/share/doc /usr/share/man /tmp/*

COPY rclone /usr/local/bin/rclone
COPY redis-tools.sh /usr/local/bin/redis-tools.sh
RUN chmod +x /usr/local/bin/redis-tools.sh

ENTRYPOINT ["redis-tools.sh"]
