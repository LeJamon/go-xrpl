FROM ubuntu:22.04

ADD --checksum=sha256:6e95ee996f847503178aa92e0d1d67be70b82308d10527616ef0e89f53597550 https://packages.xrplf.org/repository/deb-rc/pool/x/xrpld/xrpld_3.4.0~rc1-1_amd64.deb /tmp/xrpld.deb
RUN dpkg-deb -x /tmp/xrpld.deb / \
    && rm /tmp/xrpld.deb \
    && mkdir -p /var/lib/xrpld/db /var/log/xrpld

ENTRYPOINT ["/usr/bin/xrpld", "--conf", "/etc/xrpld/xrpld.cfg", "--fg"]
