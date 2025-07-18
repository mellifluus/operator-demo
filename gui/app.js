import express from 'express'
import path from 'path'
import { fileURLToPath } from 'url'
import { KubeConfig, CustomObjectsApi } from '@kubernetes/client-node'

const app = express()
app.set('view engine', 'ejs')
app.use(express.urlencoded({ extended: true }))

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
app.set('views', path.join(__dirname, 'views'))

const kc = new KubeConfig()
kc.loadFromDefault()
const k8sApi = kc.makeApiClient(CustomObjectsApi)

const group = 'tenant.core.mellifluus.io'
const version = 'v1'
const namespace = 'default'
const plural = 'tenantenvironments'

app.get('/', async (req, res) => {
  try {
    const result = await k8sApi.listNamespacedCustomObject({ group, version, namespace, plural });
    res.render('index', { crs: result.items, req });
  } catch (err) {
    console.error(err.body || err);
    res.send('Failed to load CRs');
  }
})


app.get('/create', (req, res) => {
  res.render('details', { 
    isEdit: false, 
    cr: { 
      metadata: { name: '' },
      spec: {
        displayName: '',
        replicas: 1,
        resourceQuotas: {
          cpuLimit: '2',
          memoryLimit: '4Gi',
          storageLimit: '10Gi',
          podLimit: 5
        },
        database: {
          dedicatedInstance: false
        }
      }
    }
  })
})

app.post('/create', async (req, res) => {
  try {
    const crData = {
      apiVersion: `${group}/${version}`,
      kind: 'TenantEnvironment',
      metadata: {
        name: req.body.name,
        labels: {
          'app.kubernetes.io/name': 'operator-demo',
          'app.kubernetes.io/managed-by': 'gui'
        }
      },
      spec: {
        displayName: req.body.displayName,
        replicas: parseInt(req.body.replicas),
        resourceQuotas: {
          cpuLimit: req.body.cpuLimit,
          memoryLimit: req.body.memoryLimit,
          storageLimit: req.body.storageLimit,
          podLimit: parseInt(req.body.podLimit)
        },
        database: {
          dedicatedInstance: req.body.dedicatedInstance === 'true'
        }
      }
    }

    await k8sApi.createNamespacedCustomObject({ 
      group, 
      version, 
      namespace, 
      plural, 
      body: crData 
    })
    
    res.redirect('/')
  } catch (err) {
    console.error(err.body || err)
    res.send('Failed to create CR: ' + (err.body?.message || err.message || err))
  }
})

// Route to show edit form
app.get('/edit/:name', async (req, res) => {
  try {
    const result = await k8sApi.getNamespacedCustomObject({ 
      group, 
      version, 
      namespace, 
      plural, 
      name: req.params.name 
    })
    
    res.render('details', { 
      isEdit: true, 
      cr: result 
    })
  } catch (err) {
    console.error(err.body || err)
    res.send('Failed to load CR: ' + (err.body?.message || err.message || err))
  }
})

// Route to show raw CR
app.get('/raw/:name', async (req, res) => {
  try {
    const result = await k8sApi.getNamespacedCustomObject({ 
      group, 
      version, 
      namespace, 
      plural, 
      name: req.params.name 
    })
    
    // Clean up the CR by removing managedFields and other clutter
    const cleanedCR = {
      ...result,
      metadata: {
        ...result.metadata,
        managedFields: undefined,
        resourceVersion: undefined,
        uid: undefined,
        generation: undefined,
        selfLink: undefined
      }
    }
    
    // Remove undefined fields
    Object.keys(cleanedCR.metadata).forEach(key => {
      if (cleanedCR.metadata[key] === undefined) {
        delete cleanedCR.metadata[key]
      }
    })
    
    res.render('raw', { 
      cr: result, // Keep original for status cards
      crName: req.params.name,
      rawJson: JSON.stringify(cleanedCR, null, 2)
    })
  } catch (err) {
    console.error(err.body || err)
    res.send('Failed to load CR: ' + (err.body?.message || err.message || err))
  }
})

// Route to handle edit form submission
app.post('/update/:name', async (req, res) => {
  try {
    // First get the existing CR to preserve metadata
    const existingCR = await k8sApi.getNamespacedCustomObject({ 
      group, 
      version, 
      namespace, 
      plural, 
      name: req.params.name 
    })
    
    const crData = {
      ...existingCR,
      spec: {
        displayName: req.body.displayName,
        replicas: parseInt(req.body.replicas),
        resourceQuotas: {
          cpuLimit: req.body.cpuLimit,
          memoryLimit: req.body.memoryLimit,
          storageLimit: req.body.storageLimit,
          podLimit: parseInt(req.body.podLimit)
        },
        database: {
          dedicatedInstance: req.body.dedicatedInstance === 'true'
        }
      }
    }

    await k8sApi.replaceNamespacedCustomObject({ 
      group, 
      version, 
      namespace, 
      plural, 
      name: req.params.name,
      body: crData 
    })
    
    res.redirect('/')
  } catch (err) {
    console.error(err.body || err)
    res.send('Failed to update CR: ' + (err.body?.message || err.message || err))
  }
})

app.post('/delete/:name', async (req, res) => {
  try {
    await k8sApi.deleteNamespacedCustomObject({ group, version, namespace, plural, name: req.params.name })
    res.redirect('/')
  } catch (err) {
    console.error(err.body || err)
    res.send('Failed to delete CR')
  }
})

app.listen(3001, () => {
  console.log('✅ Server running at http://localhost:3001')
})
