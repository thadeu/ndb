import type { Theme } from 'vitepress'

import Layout from './Layout.vue'
import '../../../brand/tokens.css'
import './styles/base.css'
import './styles/chrome.css'
import './styles/doc.css'
import './styles/code-tabs.css'
import './styles/landing.css'

const theme: Theme = { Layout }

export default theme
