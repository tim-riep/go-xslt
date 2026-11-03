<!-- regular expressions: matches, replace and analyze-string over every record's text -->
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:map="http://www.w3.org/2005/xpath-functions/map" xmlns:array="http://www.w3.org/2005/xpath-functions/array" xmlns:f="urn:f" exclude-result-prefixes="xs map array f">
  <xsl:output method="text"/>
  <xsl:template match="/">
    <xsl:for-each select="/orders/order[matches(customer, '^customer [0-9]*7$')]">
      <xsl:value-of select="replace(date, '(\d{4})-(\d{2})-(\d{2})', '$3.$2.$1')"/>
      <xsl:text> </xsl:text>
      <xsl:analyze-string select="string-join(items/item/@sku, ' ')" regex="s(\d+)">
        <xsl:matching-substring><xsl:value-of select="xs:integer(regex-group(1)) mod 10"/></xsl:matching-substring>
        <xsl:non-matching-substring><xsl:text>-</xsl:text></xsl:non-matching-substring>
      </xsl:analyze-string>
      <xsl:text>&#10;</xsl:text>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>
