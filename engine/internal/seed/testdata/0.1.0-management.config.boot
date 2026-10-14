firewall {
    ipv4 {
        forward {
            filter {
                default-action "drop"
                rule 5 {
                    action "accept"
                    state "established"
                    state "related"
                }
                rule 10 {
                    action "accept"
                    ipsec {
                        match-none-in
                    }
                }
            }
        }
        input {
            filter {
                default-action "drop"
                rule 1 {
                    action "drop"
                    destination {
                        port "22,443"
                    }
                    ipsec {
                        match-ipsec-in
                    }
                    protocol "tcp"
                }
                rule 5 {
                    action "accept"
                    state "established"
                    state "related"
                }
                rule 10 {
                    action "accept"
                    destination {
                        port "443"
                    }
                    protocol "tcp"
                    source {
                        address "10.244.0.0/16"
                    }
                }
            }
        }
    }
    ipv6 {
        forward {
            filter {
                default-action "drop"
            }
        }
        input {
            filter {
                default-action "drop"
                rule 5 {
                    action "accept"
                    state "established"
                    state "related"
                }
                rule 10 {
                    action "accept"
                    protocol "ipv6-icmp"
                }
            }
        }
    }
}
interfaces {
    ethernet eth0 {
        address "dhcp"
        description "pod network uplink"
    }
    loopback lo {
    }
}
pki {
    certificate site-router-api {
        certificate "MIIBWzCCAQ2gAwIBAgIBATAFBgMrZXAwJzElMCMGA1UEAxMcZ29sZGVuLm5zLmJhcmVyb3V0ZXIuaW52YWxpZDAeFw0yNjAxMDEwMDAwMDBaFw0zNjAxMDEwMDAwMDBaMCcxJTAjBgNVBAMTHGdvbGRlbi5ucy5iYXJlcm91dGVyLmludmFsaWQwKjAFBgMrZXADIQAhUvjRm3kdJEUyQuFfLqtst8/6e2pe0wCXlg4GmIHbEqNeMFwwDgYDVR0PAQH/BAQDAgeAMBMGA1UdJQQMMAoGCCsGAQUFBwMBMAwGA1UdEwEB/wQCMAAwJwYDVR0RBCAwHoIcZ29sZGVuLm5zLmJhcmVyb3V0ZXIuaW52YWxpZDAFBgMrZXADQQBLrbbidJSgTBAAikigg40nXQx+GfHUfxTjudXnHoQqxl/5Vk/UysObQ1PzKnHVWNYpTEq3cYi1r3hVY93vWVoK"
        private {
            key "MC4CAQAwBQYDK2VwBCIEIEJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJC"
        }
    }
}
service {
    https {
        api {
            keys {
                id site-router-controller {
                    key "GOLDENAPIKEY0123456789"
                }
            }
            rest {
            }
        }
        certificates {
            certificate "site-router-api"
        }
        listen-address "0.0.0.0"
    }
    ntp {
        server 0.pool.ntp.org {
        }
        server 1.pool.ntp.org {
        }
        server 2.pool.ntp.org {
        }
    }
}
system {
    config-management {
        commit-revisions "100"
    }
    console {
        device ttyS0 {
            speed "115200"
        }
    }
    host-name "golden"
    login {
        user vyos {
            authentication {
                encrypted-password "*"
            }
        }
    }
    option {
        reboot-on-upgrade-failure "5"
    }
    syslog {
        local {
            facility all {
                level "info"
            }
            facility local7 {
                level "debug"
            }
        }
    }
}


// Warning: Do not remove the following line.
// vyos-config-version: "bgp@8:broadcast-relay@1:cluster@2:config-management@1:conntrack@6:conntrack-sync@2:container@3:dhcp-relay@2:dhcp-server@11:dhcpv6-server@6:dns-dynamic@5:dns-forwarding@4:firewall@20:flow-accounting@3:https@7:ids@2:interfaces@34:ipoe-server@4:ipsec@14:isis@3:l2tp@10:lldp@3:mdns@1:monitoring@2:nat@8:nat66@3:nhrp@1:ntp@3:openconnect@3:openvpn@6:ospf@2:pim@1:pki@1:policy@9:pppoe-server@13:pptp@6:qos@3:quagga@12:reverse-proxy@3:rip@1:rpki@2:snmp@3:ssh@3:sstp@7:system@33:vpp@6:vrf@4:vrrp@4:vyos-accel-ppp@2:wanloadbalance@4:webproxy@2"
// Release version: 0.1.0
