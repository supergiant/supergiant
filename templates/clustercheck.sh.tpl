until $([ $(sudo kubectl get nodes|grep Ready|grep master|wc -l) -ge 1 ]); do printf '.'; sleep 5; done

until $([ $(sudo kubectl get nodes|grep Ready|grep none|wc -l) -ge 1 ]); do printf '.'; sleep 5; done
