Openbaleron is a simple tool meant for running as a pod or on VM image to test network communcation
As of now it's capable of doing the following:
* ICMP: - **curl pod_ip:60999/google.com/4/200** will perform icmp request to google.com, 4 times, with 200 byte packets
* HTTP GET - **curl pod_ip:60999/about.google** will make http GET to http://about.google and will follow redirects
* HTTP ping - **curl pod_ip:60999/ping** will respond *pong* 
* HTTP serve content **curl pod_ip:60999/serve/555** will respond with 555MB of random content

In case of any of those fail - it will respond with non-200 http code and tell what caused it. In case of success - 200 and `ok` response will be returned.

The main purpose of openbaleron is to easily fit into your testing infra with no additional overhead. 
Instead of writing parsers for curl or ping commands and orchestrating it with various ssh transport libraries, you just to simple GET on pods IP and it will perform what's needed and actually bring it up with kubernetes task or cloud-init.
