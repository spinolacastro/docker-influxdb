package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/template"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/fields"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/labels"
)

const defaultBrokerPort = 8086

type context struct {
	Seeds []string
}

func (c *context) Env() map[string]string {
	env := make(map[string]string)
	for _, i := range os.Environ() {
		sep := strings.Index(i, "=")
		env[i[0:sep]] = i[sep+1:]
	}
	return env
}

func main() {
	brokerPort := defaultBrokerPort
	if len(os.Getenv("INFLUXDB_BROKER_PORT")) > 0 {
		if parsedBrokerPort, err := strconv.Atoi(os.Getenv("INFLUXDB_BROKER_PORT")); err != nil {
			log.Println(err)
		} else {
			brokerPort = parsedBrokerPort
		}
	}

	addrs, err := net.InterfaceAddrs()

	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	for _, address := range addrs {
		// check the address type and if it is not a loopback the display it
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				os.Setenv("IP_ADDRESS", ipnet.IP.String())
				break
			}
		}
	}

	t, _ := template.ParseFiles("/opt/influxdb/influxdb.conf.tmpl")

	file, err := os.Create("/opt/influxdb/influxdb.conf")
	if err != nil {
		log.Fatal(err)
	}

	var seeds []string
	if len(os.Getenv("INFLUXDB_SEEDS")) == 0 {
		discoveredSeeds := discoverSeeds()
		for _, seedIP := range discoveredSeeds {
			seeds = append(seeds, fmt.Sprintf("http://%v:%d", seedIP, brokerPort))
		}
	} else {
		seeds = strings.Split(os.Getenv("INFLUXDB_SEEDS"), ",")
	}

	t.Execute(file, &context{Seeds: seeds})
	file.Close()

	cmdStr := "exec /opt/influxdb/influxd -config /opt/influxdb/influxdb.conf"

	cmdStr = os.ExpandEnv(cmdStr)

	cmd := exec.Command("/bin/sh", "-c", cmdStr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}

func discoverSeeds() []net.IP {
	var seeds []net.IP
	if len(os.Getenv("CLUSTER_DNS")) > 0 {
		seeds = discoverSeedsFromDNS()
	} else {
		seeds = discoverSeedsFromKubernetesMaster()
	}
	return seeds
}

func discoverSeedsFromDNS() []net.IP {
	ips, err := net.LookupIP(os.Getenv("CLUSTER_DNS"))
	if err != nil {
		log.Println(err)
		return nil
	}
	return ips
}

func discoverSeedsFromKubernetesMaster() []net.IP {
	var seeds []net.IP

	kubeMaster := os.ExpandEnv("${KUBERNETES_SERVICE_HOST}:${KUBERNETES_SERVICE_PORT}")

	if len(os.Getenv("KUBERNETES_MASTER")) > 0 {
		kubeMaster = os.Getenv("KUBERNETES_MASTER")
	}
	kubeMaster = os.ExpandEnv(kubeMaster)

	if len(os.Getenv("KUBERNETES_SELECTOR")) > 0 && len(kubeMaster) > 0 {
		if !(strings.HasPrefix(kubeMaster, "http://") || strings.HasPrefix(kubeMaster, "https://")) {
			kubeMaster = "https://" + kubeMaster
		}
		insecure, _ := strconv.ParseBool(os.Getenv("KUBERNETES_INSECURE"))
		kubeClient := client.NewOrDie(&client.Config{
			Host:     os.ExpandEnv(kubeMaster),
			Insecure: len(os.Getenv("KUBERNETES_INSECURE")) > 0 && insecure,
		})

		selector, err := labels.Parse(os.Getenv("KUBERNETES_SELECTOR"))
		if err != nil {
			log.Println(err)
		} else {
			namespace := os.Getenv("KUBERNETES_NAMESPACE")
			if len(namespace) == 0 {
				namespace = api.NamespaceDefault
			}

			podList, err := kubeClient.Pods(namespace).List(selector, fields.Everything())
			if err != nil {
				log.Println(err)
			} else {
				currentHostname, err := os.Hostname()
				if err != nil {
					log.Println(err)
				}
				for _, pod := range podList.Items {
					if pod.Status.Phase == api.PodRunning && len(pod.Status.PodIP) > 0 && pod.Name != currentHostname {
						podIP := net.ParseIP(pod.Status.PodIP)
						if podIP != nil {
							seeds = append(seeds, podIP)
						}
					}
				}
			}
		}
	}
	return seeds
}
