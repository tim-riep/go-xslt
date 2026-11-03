<!-- number formatting and arithmetic: format-number, format-integer, round, xs:decimal sums per record -->
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:map="http://www.w3.org/2005/xpath-functions/map" xmlns:array="http://www.w3.org/2005/xpath-functions/array" xmlns:f="urn:f" exclude-result-prefixes="xs map array f">
  <xsl:output method="text"/>
  <xsl:decimal-format name="de" decimal-separator="," grouping-separator="."/>
  <xsl:template match="/">
    <xsl:for-each select="/orders/order">
      <xsl:variable name="p" as="xs:decimal" select="xs:decimal(price)"/>
      <xsl:variable name="q" as="xs:integer" select="sum(items/item/xs:integer(@qty))"/>
      <xsl:value-of select="concat(format-number($p * $q, '#.##0,00', 'de'), ' ', format-integer($q, '000'), ' ', round($p div 3, 2), ' ', format-number($p * 1.19, '0.00'), '&#10;')"/>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>
