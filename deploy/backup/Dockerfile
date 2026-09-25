# pg_dump/pg_restore matching the server, plus age for encryption.
FROM postgres:17-alpine@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24
RUN apk add --no-cache age
COPY backup.sh restore.sh /usr/local/bin/
ENTRYPOINT []
CMD ["backup.sh"]
