import js from '@eslint/js'
import globals from 'globals'
import stylistic from '@stylistic/eslint-plugin'
import tseslint from 'typescript-eslint'
import vue from 'eslint-plugin-vue'
import vueParser from 'vue-eslint-parser'

const blank = (blankLine, prev, next) => ({ blankLine, prev, next })

export default [
  { ignores: ['dist/', 'node_modules/', '.vitepress/cache/', '.vitepress/dist/'] },
  js.configs.recommended,
  ...vue.configs['flat/essential'],
  ...tseslint.configs.recommended,
  { languageOptions: { globals: { ...globals.browser, ...globals.node } } },
  {
    files: ['**/*.vue'],
    languageOptions: { parser: vueParser, parserOptions: { parser: tseslint.parser } },
  },
  {
    plugins: { '@stylistic': stylistic },
    rules: {
      '@stylistic/padding-line-between-statements': [
        'error',
        blank('always', '*', 'return'),
        blank('always', '*', ['if', 'for', 'while', 'do', 'switch', 'try']),
        blank('always', ['if', 'for', 'while', 'do', 'switch', 'try'], '*'),
        blank('always', ['const', 'let', 'var'], '*'),
        blank('any', ['const', 'let', 'var'], ['const', 'let', 'var']),
        blank('always', '*', ['function', 'class', 'export']),
        blank('always', ['function', 'class', 'export'], '*'),
        blank('any', 'export', 'export'),
        blank('always', 'import', '*'),
        blank('any', 'import', 'import'),
      ],
      'no-inline-comments': 'warn',
      'vue/multi-word-component-names': 'off',
      curly: ['warn', 'multi-line'],
      'no-console': ['warn', { allow: ['warn', 'error'] }],
      'prefer-const': 'warn',
      'no-var': 'error',
    },
  },
]
