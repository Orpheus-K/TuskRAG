FROM paradedb/paradedb:v0.22.6-pg17@sha256:58bf87d2a6f1e72f56590b0f0ae1467e82c3f99ca203dde646672c8ac13148b2 AS extensions

FROM postgres:17.9-trixie@sha256:2a0d0fe14825b0939f78a8cad5cd4e6aa68bf94d0e5dd96e24b6d23af4315545
COPY --from=extensions /usr/lib/postgresql/17/lib/pg_search.so /usr/lib/postgresql/17/lib/pg_search.so
COPY --from=extensions /usr/lib/postgresql/17/lib/vector.so /usr/lib/postgresql/17/lib/vector.so
COPY --from=extensions /usr/share/postgresql/17/extension/pg_search* /usr/share/postgresql/17/extension/
COPY --from=extensions /usr/share/postgresql/17/extension/vector* /usr/share/postgresql/17/extension/
COPY licenses/ /usr/share/doc/tuskrag/licenses/

COPY 01-tuskrag.sh /docker-entrypoint-initdb.d/01-tuskrag.sh
RUN chmod 0755 /docker-entrypoint-initdb.d/01-tuskrag.sh \
    && printf "\nshared_preload_libraries = 'pg_search'\n" >> /usr/share/postgresql/postgresql.conf.sample
