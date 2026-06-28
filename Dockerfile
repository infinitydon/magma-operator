FROM python:3.12-slim

ARG HELM_VERSION=3.15.4

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git tar gzip \
    && curl -fsSL "https://get.helm.sh/helm-v${HELM_VERSION}-linux-amd64.tar.gz" \
      | tar -xz -C /tmp \
    && mv /tmp/linux-amd64/helm /usr/local/bin/helm \
    && rm -rf /var/lib/apt/lists/* /tmp/linux-amd64

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY magma_operator ./magma_operator

USER 65532:65532
ENTRYPOINT ["kopf", "run", "-m", "magma_operator.main", "--standalone"]
