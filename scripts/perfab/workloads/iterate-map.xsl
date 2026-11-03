<!-- XSLT 3.0: xsl:iterate with a map(xs:string, xs:decimal) parameter, map:put per record, 5,000 keys -->
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:map="http://www.w3.org/2005/xpath-functions/map" xmlns:array="http://www.w3.org/2005/xpath-functions/array" xmlns:f="urn:f" exclude-result-prefixes="xs map array f">
  <xsl:output method="text"/>
  <xsl:template match="/">
    <xsl:iterate select="/orders/order">
      <xsl:param name="totals" as="map(xs:string, xs:decimal)" select="map{}"/>
      <xsl:on-completion>
        <xsl:for-each select="sort(map:keys($totals))"><xsl:value-of select="concat(., ' ', $totals(.), '&#10;')"/></xsl:for-each>
      </xsl:on-completion>
      <xsl:next-iteration>
        <xsl:with-param name="totals" select="map:put($totals, string(customer), ($totals(string(customer)), 0)[1] + xs:decimal(price))"/>
      </xsl:next-iteration>
    </xsl:iterate>
  </xsl:template>
</xsl:stylesheet>
