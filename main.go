package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"sync"

	"github.com/miekg/dns"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var (
	kubeClient    *kubernetes.Clientset
	dnsPort       = getEnv("DNS_PORT", "53")
	fallbackDNS   = "1.1.1.1:53"
	wildcardRegex = regexp.MustCompile(`^\*\.(?P<anydomain>[^*]+)$`)
)

func main() {
	initKubeClient()

	dns.HandleFunc(".", handleDNSRequest)

	server := &dns.Server{Addr: ":" + dnsPort, Net: "udp"}
	log.Printf("Starting DNS server on %s\n", server.Addr)

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func initKubeClient() {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Failed to create in-cluster config: %v", err)
	}

	kubeClient, err = kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to create kubernetes client: %v", err)
	}
}

func handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	msg := dns.Msg{}
	msg.SetReply(r)

	var wg sync.WaitGroup
	for _, q := range msg.Question {
		wg.Add(1)
		go func(q dns.Question) {
			defer wg.Done()
			processQuery(&msg, q)
		}(q)
	}
	wg.Wait()

	w.WriteMsg(&msg)
}

func processQuery(m *dns.Msg, q dns.Question) {
	if q.Qtype != dns.TypeA {
		return // For simplicity, only handle TypeA queries
	}

	name := q.Name[:len(q.Name)-1] // Remove trailing dot
	log.Printf("-------------------------------\n")
	log.Printf("Query: %v\n", name)
	ingresses, err := fetchIngresses()
	if err != nil {
		log.Printf("Error fetching ingresses: %v\n", err)
		return
	}

	confirmed, fallbackRequired := matchIngress(ingresses, name)

	for range confirmed {
		rr, err := dns.NewRR(fmt.Sprintf("%s A %s", q.Name, os.Getenv("INGRESS_IP")))
		if err == nil {
			log.Printf("Answer: %v\n", rr.String())
			m.Answer = append(m.Answer, rr)
		}
	}

	if fallbackRequired {
		queryFallbackDNS(name, m)
	}
}

func fetchIngresses() ([]networkingv1.Ingress, error) {
	list, err := kubeClient.NetworkingV1().Ingresses("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func matchIngress(ingresses []networkingv1.Ingress, name string) ([]string, bool) {
	var confirmedNames []string
	fallbackRequired := false

	for _, ingress := range ingresses {
		for _, rule := range ingress.Spec.Rules {
			if name == rule.Host {
				confirmedNames = append(confirmedNames, rule.Host)
			} else if wildcardRegex.MatchString(rule.Host) {
				matches := wildcardRegex.FindStringSubmatch(rule.Host)
				domainPattern := matches[1]
				if matched, _ := regexp.MatchString(domainPattern, name); matched {
					confirmedNames = append(confirmedNames, name)
				}
			}
		}
	}

	if len(confirmedNames) == 0 {
		fallbackRequired = true
	}

	return confirmedNames, fallbackRequired
}

func queryFallbackDNS(name string, m *dns.Msg) {
	c := new(dns.Client)
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(name), dns.TypeA)
	r, _, err := c.Exchange(msg, fallbackDNS)
	if err != nil {
		log.Printf("Fallback DNS query failed: %v\n", err)
		return
	}
	for _, ans := range r.Answer {
		log.Printf("Answer: %v\n", ans.String())
		m.Answer = append(m.Answer, ans)
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
