ARG BASE_IMAGE=claude-contained:latest
FROM ${BASE_IMAGE}

ARG JBR_VERSION=25.0.1
ARG JBR_BUILD=b268.52
ARG JBR_FLAVOR=jbrsdk
ARG JBR_SHA256_ARM64=1572780fd316486df0cac2d48f1647d0407a261389460be36e01a55cae84aaff
ARG JBR_SHA256_AMD64=820263435011ed95ce939da2d54b36d3ec02001fba2a3e3310fcf09048f2f45a
ARG HOTSWAP_AGENT_VERSION=2.0.3
ARG JDTLS_VERSION=1.40.0
ARG JDTLS_TIMESTAMP=202409261450
ARG MAVEN_VERSION=3.9.11
ARG JBANG_VERSION=0.141.0

RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      arm64) jbr_arch=aarch64; jbr_sha256="$JBR_SHA256_ARM64" ;; \
      amd64) jbr_arch=x64; jbr_sha256="$JBR_SHA256_AMD64" ;; \
      *) echo "Unsupported architecture: $arch" >&2; exit 1 ;; \
    esac; \
    file="${JBR_FLAVOR}-${JBR_VERSION}-linux-${jbr_arch}-${JBR_BUILD}.tar.gz"; \
    curl -fL "https://cache-redirector.jetbrains.com/intellij-jbr/${file}" -o /tmp/jbr.tar.gz; \
    printf '%s  %s\n' "$jbr_sha256" /tmp/jbr.tar.gz | sha256sum -c -; \
    tar -tzf /tmp/jbr.tar.gz >/dev/null; \
    mkdir -p /opt/jbr; \
    tar -xzf /tmp/jbr.tar.gz -C /opt/jbr --strip-components=1; \
    rm /tmp/jbr.tar.gz; \
    /opt/jbr/bin/java -version

RUN set -eux; \
    base="https://repo1.maven.org/maven2/org/hotswapagent/hotswap-agent/${HOTSWAP_AGENT_VERSION}"; \
    file="hotswap-agent-${HOTSWAP_AGENT_VERSION}.jar"; \
    curl -fL "${base}/${file}" -o "/tmp/${file}"; \
    curl -fL "${base}/${file}.sha256" -o /tmp/hotswap.sha256; \
    printf '%s  %s\n' "$(awk '{print $1}' /tmp/hotswap.sha256)" "/tmp/${file}" | sha256sum -c -; \
    mkdir -p /opt/jbr/lib/hotswap; \
    mv "/tmp/${file}" /opt/jbr/lib/hotswap/hotswap-agent.jar; \
    rm /tmp/hotswap.sha256

COPY hotswap-agent.properties /opt/jbr/lib/hotswap/hotswap-agent.properties

RUN set -eux; \
    base="https://download.eclipse.org/jdtls/milestones/${JDTLS_VERSION}"; \
    file="jdt-language-server-${JDTLS_VERSION}-${JDTLS_TIMESTAMP}.tar.gz"; \
    curl -fL "${base}/${file}" -o "/tmp/${file}"; \
    curl -fL "${base}/${file}.sha256" -o /tmp/jdtls.sha256; \
    printf '%s  %s\n' "$(awk '{print $1}' /tmp/jdtls.sha256)" "/tmp/${file}" | sha256sum -c -; \
    mkdir -p /opt/jdtls; \
    tar -xzf "/tmp/${file}" -C /opt/jdtls; \
    rm "/tmp/${file}" /tmp/jdtls.sha256

RUN set -eux; \
    base="https://archive.apache.org/dist/maven/maven-3/${MAVEN_VERSION}/binaries"; \
    file="apache-maven-${MAVEN_VERSION}-bin.tar.gz"; \
    curl -fL "${base}/${file}" -o "/tmp/${file}"; \
    curl -fL "${base}/${file}.sha512" -o /tmp/maven.sha512; \
    printf '%s  %s\n' "$(awk '{print $1}' /tmp/maven.sha512)" "/tmp/${file}" | sha512sum -c -; \
    mkdir -p /opt/maven; \
    tar -xzf "/tmp/${file}" -C /opt/maven --strip-components=1; \
    rm "/tmp/${file}" /tmp/maven.sha512; \
    JAVA_HOME=/opt/jbr /opt/maven/bin/mvn --version

RUN set -eux; \
    base="https://github.com/jbangdev/jbang/releases/download/v${JBANG_VERSION}"; \
    file="jbang-${JBANG_VERSION}.zip"; \
    curl -fL "${base}/${file}" -o "/tmp/${file}"; \
    curl -fL "${base}/${file}.sha256" -o /tmp/jbang.sha256; \
    printf '%s  %s\n' "$(awk '{print $1}' /tmp/jbang.sha256)" "/tmp/${file}" | sha256sum -c -; \
    unzip -q "/tmp/${file}" -d /opt; \
    mv "/opt/jbang-${JBANG_VERSION}" /opt/jbang; \
    chmod +x /opt/jbang/bin/jbang; \
    rm "/tmp/${file}" /tmp/jbang.sha256; \
    /opt/jbang/bin/jbang version

RUN set -eux; \
    ln -sf /opt/jbr/bin/java /usr/local/bin/java; \
    ln -sf /opt/jbr/bin/javac /usr/local/bin/javac; \
    ln -sf /opt/jbr/bin/jar /usr/local/bin/jar; \
    ln -sf /opt/maven/bin/mvn /usr/local/bin/mvn; \
    ln -sf /opt/jbang/bin/jbang /usr/local/bin/jbang; \
    ln -sf /opt/jdtls/bin/jdtls /usr/local/bin/jdtls; \
    mkdir -p /home/dev/.claude-contained/cache/maven /etc/claude-contained/env.d; \
    chown -R dev:dev /home/dev/.claude-contained

COPY 10-java /etc/claude-contained/env.d/10-java
COPY mavenrc /usr/local/etc/mavenrc

ENV JAVA_HOME=/opt/jbr
ENV JAVA_TOOL_OPTIONS="-XX:+UseG1GC -XX:+AllowEnhancedClassRedefinition -XX:+ClassUnloading -XX:HotswapAgent=fatjar --add-opens=java.base/java.lang=ALL-UNNAMED --add-opens=java.base/java.lang.reflect=ALL-UNNAMED --add-opens=java.base/java.io=ALL-UNNAMED --add-opens=java.base/sun.nio.ch=ALL-UNNAMED --add-opens=java.base/sun.security.action=ALL-UNNAMED --add-opens=java.base/jdk.internal.loader=ALL-UNNAMED --add-opens=java.desktop/java.beans=ALL-UNNAMED --add-opens=java.desktop/com.sun.beans=ALL-UNNAMED --add-opens=java.desktop/com.sun.beans.introspect=ALL-UNNAMED --add-opens=java.desktop/com.sun.beans.util=ALL-UNNAMED -Dvaadin.productionMode=false -Dspring.devtools.restart.enabled=false"
ENV MAVEN_OPTS="-Dmaven.repo.local=\$HOME/.claude-contained/cache/maven"
ENV PATH="/opt/jbr/bin:/opt/maven/bin:/opt/jbang/bin:${PATH}"
