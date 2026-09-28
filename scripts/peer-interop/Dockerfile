FROM ubuntu:22.04

ADD --checksum=sha256:4dc40a21b12b7d21f2760b16545c1d842962a96978dd3199eb8de9aa0aff8339 https://packages.xrplf.org/repository/deb-stable/pool/x/xrpld/xrpld_3.4.0-1_amd64.deb /tmp/xrpld.deb
RUN dpkg-deb -x /tmp/xrpld.deb / \
    && rm /tmp/xrpld.deb \
    && mkdir -p /var/lib/xrpld/db /var/log/xrpld

ENTRYPOINT ["/usr/bin/xrpld", "--conf", "/etc/xrpld/xrpld.cfg", "--fg"]
